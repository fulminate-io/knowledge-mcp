// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_boundary_dispatch_test.go is the Phase 9 Step 2 regression guard for
// the dispatcher-level boundary filter wired into dispatchIAMRules. These
// tests build a synthetic account with ONE escalation rule's target shape
// (iam:CreateLoginProfile + a victim user) and verify that the rule's
// emitted edge is suppressed when the source principal has a restrictive
// boundary, and preserved when the boundary is permissive.
//
// The iam_boundary_test.go file covers the primitive helpers
// (parseBoundaryPolicy, actionAllowedWithinBoundary) as pure unit tests.
// THIS file covers the end-to-end wire-up through the dispatcher, which
// is the only surface where a false positive would ever reach a finding.

// attachBoundary writes a URL-encoded boundary policy to the
// permission_boundary metadata key of an existing principal — mirrors what
// cloud/aws/iam_boundary.go does on the collector side. Reuses the upsert
// pattern from addInlinePolicy so we do not reopen the fixture.
func attachBoundary(t *testing.T, fx *cloudFixture, principalARN, doc string) {
	t.Helper()
	require.True(t, fx.hasNode(accountA, principalARN),
		"principal %s must exist before attaching boundary", principalARN)
	fx.setNodeMeta(accountA, principalARN, permissionBoundaryMetaKey, url.QueryEscape(doc))
}

// TestDispatchIAMRules_BoundaryBlocksCreateLoginProfile verifies the
// dispatcher drops a createLoginProfileRule edge when the source principal
// has an identity policy that allows iam:CreateLoginProfile (so the rule
// body fires) but a permission boundary that does NOT include
// iam:CreateLoginProfile (so AWS evaluation would block the action).
//
// Pre-Phase-9-Step-2 this scenario produced a false positive: the analyzer
// emitted an impersonate edge and a downstream finding even though the
// principal's effective permissions could never actually execute the
// escalation. The regression bar is: zero edges from the attacker in the
// inferred map.
func TestDispatchIAMRules_BoundaryBlocksCreateLoginProfile(t *testing.T) {
	fx := newCloudFixture(t)

	// Attacker has identity-side iam:CreateLoginProfile (rule fires) plus a
	// boundary that only grants s3:* (rule output must be suppressed).
	const attackerARN = "arn:aws:iam::111111111111:user/attacker-with-boundary"
	addIAMUserWithInline(t, fx, accountA, attackerARN, "attacker-with-boundary",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)
	attachBoundary(t, fx, attackerARN,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)

	// Victim user present so the rule has a target to emit an edge toward.
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	inferred, _, err := dispatchIAMRules(newTestCtx(t), rctx)
	require.NoError(t, err)

	// The dispatcher MUST have dropped every edge the rule emitted — the
	// boundary does not permit iam:CreateLoginProfile, so AWS evaluation
	// would block the action even though the identity policy allows it.
	for src, edges := range inferred {
		for _, e := range edges {
			if e.RuleName != "create_login_profile" {
				continue
			}
			t.Errorf("boundary-restricted principal %s produced a create_login_profile edge: %+v", src, e)
		}
	}
}

// TestDispatchIAMRules_BoundaryPermitsCreateLoginProfile is the positive
// half of the regression guard: the attacker has both identity AND a
// boundary that explicitly allows iam:CreateLoginProfile, so the rule MUST
// still fire and the dispatcher MUST preserve the emitted edge. This pins
// the "boundary does not over-suppress" contract — a boundary that allows
// the action is functionally identical to having no boundary from the
// analyzer's point of view.
func TestDispatchIAMRules_BoundaryPermitsCreateLoginProfile(t *testing.T) {
	fx := newCloudFixture(t)

	const attackerARN = "arn:aws:iam::111111111111:user/attacker-bounded-allow"
	addIAMUserWithInline(t, fx, accountA, attackerARN, "attacker-bounded-allow",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)
	// Permissive boundary that covers the action — matches identity exactly.
	attachBoundary(t, fx, attackerARN,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)

	const victimARN = "arn:aws:iam::111111111111:user/victim"
	addIAMUser(t, fx, accountA, victimARN, "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	inferred, _, err := dispatchIAMRules(newTestCtx(t), rctx)
	require.NoError(t, err)

	// One create_login_profile edge must survive — attacker → victim.
	var found bool
	for _, edges := range inferred {
		for _, e := range edges {
			if e.RuleName != "create_login_profile" {
				continue
			}
			if e.FromID == attackerARN && e.ToID == victimARN {
				found = true
			}
		}
	}
	assert.True(t, found, "permissive-boundary attacker must still emit create_login_profile → victim edge")
}
