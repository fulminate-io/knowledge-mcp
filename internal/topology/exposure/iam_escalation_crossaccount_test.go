// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_crossaccount_test.go is the Phase 9.5 end-to-end test:
// two AWS accounts wired together via an sts:AssumeRole trust policy so
// that a non-admin principal in account A reaches AdministratorAccess
// via a role in account B.
//
// Fixture shape:
//
//	Account A (111111111111):
//	  user/alice            — plain IAM user, no inline policies
//
//	Account B (222222222222):
//	  role/target           — trust policy allows A:user/alice to assume
//	                          AdministratorAccess attached
//
// Expected result: IAMEscalationAnalyzer.Run (req.Name = A) emits ONE
// finding describing alice → target, with has_cross_account=1 metric,
// both account IDs visible in Evidence, and the buildPMapperNarrative
// Summary naming both principals across the account boundary.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestIAMEscalationAnalyzer_CrossAccountAssumeRole_EndToEnd is the
// Phase 9.5 headline test. alice in account A assumes target in
// account B which has AdministratorAccess — end-to-end the analyzer
// must surface exactly one cross-account escalation finding.
func TestIAMEscalationAnalyzer_CrossAccountAssumeRole_EndToEnd(t *testing.T) {
	fx := newCloudFixture(t)

	// Account A: alice (plain user, no policies)
	aliceARN := "arn:aws:iam::111111111111:user/alice"
	addIAMUser(t, fx, accountA, aliceARN, "alice")

	// Account B: target role with trust policy allowing alice from A,
	// and AdministratorAccess managed-policy attachment.
	targetARN := "arn:aws:iam::222222222222:role/target"
	addIAMRoleWithTrust(t, fx, accountB, targetARN, "target",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+aliceARN+`"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountB, targetARN)

	// Run the analyzer against account A — the primary analysis target.
	findings := runAnalyzer(t, fx, accountA, 0)
	require.Len(t, findings, 1, "exactly one cross-account escalation finding expected")

	f := findings[0]
	assert.Equal(t, SeverityCritical, f.Severity)
	assert.InDelta(t, 1, f.Metrics["hop_count"], 1e-9, "alice → target is a single hop")
	assert.InDelta(t, 1, f.Metrics["has_cross_account"], 1e-9, "cross-account flag must be set")

	// Evidence ordering: source first, then the target, then any
	// dedup rule tokens appended by iam_escalation_dedup.go.
	require.GreaterOrEqual(t, len(f.Evidence), 2, "evidence must include source and target nodes")
	assert.Equal(t, aliceARN, f.Evidence[0], "primary evidence is alice")
	assert.Equal(t, targetARN, f.Evidence[1], "second evidence is target")

	// Summary should name both principals — buildPMapperNarrative emits
	// "<from> can assume role <to>, reaching AdministratorAccess." The
	// principals resolve via ResolveNodeName, which falls back to the
	// raw ARN when SymbolName is not available, so either the symbolic
	// names or the ARNs must be present.
	summary := f.Summary
	assert.NotEmpty(t, summary, "cross-account finding must have a narrative Summary")
	assert.Contains(t, summary, terminalAdminState, "narrative must terminate in AdministratorAccess")
	assert.True(t,
		strings.Contains(summary, "alice") || strings.Contains(summary, aliceARN),
		"narrative must reference alice: %s", summary)
	assert.True(t,
		strings.Contains(summary, "target") || strings.Contains(summary, targetARN),
		"narrative must reference target: %s", summary)

	// The narrative should also carry the verb-phrase for assume-role
	// (humanKindLabel(iamEdgeAssumeRole) == "can assume role"). If this
	// assertion ever trips it means the path went through a different
	// edge kind than we designed for — a regression worth investigating.
	assert.Contains(t, summary, "can assume role",
		"cross-account hop must render as the assume-role verb: %s", summary)
}

// TestIAMEscalationAnalyzer_CrossAccountCycleTerminates extends the
// unit-level BFS cycle test with a full analyzer run: two accounts
// with mutually trusting roles and NO admin anywhere must not loop
// and must produce no findings.
func TestIAMEscalationAnalyzer_CrossAccountCycleTerminates(t *testing.T) {
	fx := newCloudFixture(t)

	aRoleARN := "arn:aws:iam::111111111111:role/a-role"
	bRoleARN := "arn:aws:iam::222222222222:role/b-role"

	// a-role in A trusts b-role in B
	addIAMRoleWithTrust(t, fx, accountA, aRoleARN, "a-role",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+bRoleARN+`"},"Action":"sts:AssumeRole"}]}`)
	// b-role in B trusts a-role in A — the back edge that forms the cycle
	addIAMRoleWithTrust(t, fx, accountB, bRoleARN, "b-role",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+aRoleARN+`"},"Action":"sts:AssumeRole"}]}`)

	// Neither role has admin attached — no escalation target exists.
	findings := runAnalyzer(t, fx, accountA, 0)
	assert.Empty(t, findings, "cycle with no admin must terminate cleanly with zero findings")
}

// TestIAMEscalationAnalyzer_CrossAccountMultiHop verifies the BFS can
// traverse more than one account boundary in a single path: A has
// alice, A also has a bridge role that trusts alice, and B has an
// admin role that trusts the bridge. alice → bridge → target(admin).
// Exercises back-to-back cross-account hops (well, one same-account
// hop plus one cross-account hop) end-to-end through the merged
// inferred-edge map.
func TestIAMEscalationAnalyzer_CrossAccountMultiHop(t *testing.T) {
	fx := newCloudFixture(t)

	aliceARN := "arn:aws:iam::111111111111:user/alice"
	bridgeARN := "arn:aws:iam::111111111111:role/bridge"
	targetARN := "arn:aws:iam::222222222222:role/admin-b"

	addIAMUser(t, fx, accountA, aliceARN, "alice")
	addIAMRoleWithTrust(t, fx, accountA, bridgeARN, "bridge",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+aliceARN+`"},"Action":"sts:AssumeRole"}]}`)
	addIAMRoleWithTrust(t, fx, accountB, targetARN, "admin-b",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+bridgeARN+`"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountB, targetARN)

	findings := runAnalyzer(t, fx, accountA, 0)
	require.NotEmpty(t, findings)

	// Find the alice-sourced finding.
	var match *Finding
	for i := range findings {
		if findings[i].Evidence[0] == aliceARN {
			match = &findings[i]
			break
		}
	}
	require.NotNil(t, match, "expected a finding sourced from alice")
	assert.InDelta(t, 2, match.Metrics["hop_count"], 1e-9, "alice → bridge → target is 2 hops")
	assert.InDelta(t, 1, match.Metrics["has_cross_account"], 1e-9, "must flag cross-account path")
}

// Compile-time reference to store package to avoid import removal if
// later edits trim the direct uses above. The end-to-end test uses the
// store package transitively via the fixture helpers.
var _ = kgtypes.GraphCloud
