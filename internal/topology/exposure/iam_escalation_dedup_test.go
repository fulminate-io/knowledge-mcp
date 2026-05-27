// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_dedup_test.go covers Phase 9 Step 3: dedupFindings merges
// escalation paths sharing the same (source, terminal) key into one
// finding whose Evidence enumerates every contributing rule and whose
// min_confidence metric is the LOWEST per-rule confidence.
//
// Two layers of coverage:
//
//  1. Unit tests against dedupFindings directly with synthetic paths and
//     inferred maps — lets us pin merge semantics without building a
//     full cloud fixture for every edge shape.
//
//  2. End-to-end fixture that wires one principal with two distinct
//     permissions reaching the same admin role, exercising the whole
//     analyzer Run path and asserting a single merged finding.

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestDedupFindings_MultipleRulesSameTerminal verifies that when two
// rules independently emit an edge from dev → admin-role, dedupFindings
// produces exactly ONE finding whose Evidence enumerates both rule
// names (ba83: sorted order, prefixed with "rule:").
func TestDedupFindings_MultipleRulesSameTerminal(t *testing.T) {
	src := "arn:aws:iam::111111111111:user/dev"
	tgt := "arn:aws:iam::111111111111:role/admin"

	path := escalationPath{
		Source: src,
		Target: tgt,
		Nodes:  []string{src, tgt},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: tgt, Kind: iamEdgeExecuteAs, RuleName: "put_role_policy", Confidence: 1.0},
		},
	}
	inferred := map[string][]iamInferredEdge{
		src: {
			{FromID: src, ToID: tgt, Kind: iamEdgeExecuteAs, RuleName: "put_role_policy", Confidence: 1.0},
			{FromID: src, ToID: tgt, Kind: iamEdgeExecuteAs, RuleName: "create_policy_version", Confidence: 1.0},
		},
	}

	findings := dedupFindings(t.Context(), Request{}, []escalationPath{path}, inferred)
	require.Len(t, findings, 1, "expected exactly one finding for a single (source, terminal) key")

	f := findings[0]
	ruleTokens := evidenceRuleTokens(f)
	assert.ElementsMatch(t, []string{"create_policy_version", "put_role_policy"}, ruleTokens,
		"expected both contributing rules in Evidence")
	assert.InDelta(t, 2.0, f.Metrics["contributing_rules"], 1e-9)
	assert.InDelta(t, 1.0, f.Metrics["min_confidence"], 1e-9)
}

// TestDedupFindings_MinConfidenceAcrossRules verifies that merging
// rules with different confidences produces a min_confidence equal to
// the LOWEST value — weakest-link semantics.
func TestDedupFindings_MinConfidenceAcrossRules(t *testing.T) {
	src := "arn:aws:iam::111111111111:user/dev"
	tgt := "arn:aws:iam::111111111111:role/admin"

	path := escalationPath{
		Source: src,
		Target: tgt,
		Nodes:  []string{src, tgt},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: tgt, Kind: iamEdgeImpersonate, RuleName: "create_policy_version", Confidence: 1.0},
		},
	}
	inferred := map[string][]iamInferredEdge{
		src: {
			{FromID: src, ToID: tgt, Kind: iamEdgeImpersonate, RuleName: "create_policy_version", Confidence: 1.0},
			// set_default_policy_version's 0.7 confidence should pull min down.
			{FromID: src, ToID: tgt, Kind: iamEdgeImpersonate, RuleName: "set_default_policy_version", Confidence: 0.7},
		},
	}

	findings := dedupFindings(t.Context(), Request{}, []escalationPath{path}, inferred)
	require.Len(t, findings, 1)
	assert.InDelta(t, 0.7, findings[0].Metrics["min_confidence"], 1e-9,
		"min_confidence must equal the lowest contributing rule's confidence")
}

// TestDedupFindings_DistinctTerminalsStaySeparate verifies that a source
// reaching two DIFFERENT admin targets produces two findings, not one —
// the dedup key is (source, terminal), not source alone.
func TestDedupFindings_DistinctTerminalsStaySeparate(t *testing.T) {
	src := "arn:aws:iam::111111111111:user/dev"
	admin1 := "arn:aws:iam::111111111111:role/admin-a"
	admin2 := "arn:aws:iam::111111111111:role/admin-b"

	p1 := escalationPath{
		Source: src,
		Target: admin1,
		Nodes:  []string{src, admin1},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: admin1, Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy", Confidence: 1.0},
		},
	}
	p2 := escalationPath{
		Source: src,
		Target: admin2,
		Nodes:  []string{src, admin2},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: admin2, Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy", Confidence: 1.0},
		},
	}
	findings := dedupFindings(t.Context(), Request{}, []escalationPath{p1, p2}, map[string][]iamInferredEdge{})
	require.Len(t, findings, 2, "distinct terminals must produce separate findings")
}

// TestDedupFindings_MergeTwoPathsSameKey verifies that TWO paths sharing
// the same (source, terminal) merge into one finding (not two). This is
// a direct test of the merge branch in dedupFindings.
func TestDedupFindings_MergeTwoPathsSameKey(t *testing.T) {
	src := "dev"
	tgt := "admin"
	base := escalationPath{
		Source: src,
		Target: tgt,
		Nodes:  []string{src, tgt},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: tgt, Kind: iamEdgeImpersonate, RuleName: "rule_a", Confidence: 0.9},
		},
	}
	alt := escalationPath{
		Source: src,
		Target: tgt,
		Nodes:  []string{src, tgt},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: tgt, Kind: iamEdgeImpersonate, RuleName: "rule_b", Confidence: 0.7},
		},
	}
	findings := dedupFindings(t.Context(), Request{}, []escalationPath{base, alt}, map[string][]iamInferredEdge{})
	require.Len(t, findings, 1, "same (source, terminal) must merge into one finding")
	ruleTokens := evidenceRuleTokens(findings[0])
	assert.ElementsMatch(t, []string{"rule_a", "rule_b"}, ruleTokens)
	assert.InDelta(t, 0.7, findings[0].Metrics["min_confidence"], 1e-9)
}

// TestIAMEscalation_DedupEndToEnd wires a fixture where one user has
// BOTH iam:UpdateAssumeRolePolicy AND iam:PassRole on admin-role, and
// admin-role trusts ec2.amazonaws.com. Two rules independently emit an
// execute_as edge from dev → admin-role:
//
//   - updateAssumeRolePolicyRule (rewrite trust, 1.0)
//   - runInstancesRule            (PassRole + ec2 trust, 0.9)
//
// The analyzer must emit exactly one finding whose Evidence lists both
// rule names and whose min_confidence is 0.9 (weakest link).
//
// We deliberately avoid rules like putRolePolicy that emit a
// self-loop on the caller — those promote dev to admin and remove it
// from the source set, hiding the dedup path.
func TestIAMEscalation_DedupEndToEnd(t *testing.T) {
	fx := newCloudFixture(t)

	// admin-role trusts ec2 so run_instances fires on dev.
	addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	// dev has both UpdateAssumeRolePolicy and PassRole permissions on *.
	// Neither rule emits a self-loop, so dev remains a valid BFS source.
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/dev", "dev", "esc",
		`{"Statement":[{"Effect":"Allow","Action":["iam:UpdateAssumeRolePolicy","iam:PassRole"],"Resource":"*"}]}`)

	findings := runAnalyzer(t, fx, accountA, 0)

	// Find all findings sourced from dev. There must be exactly one.
	var devFindings []Finding
	for i := range findings {
		if findings[i].Evidence[0] == "arn:aws:iam::111111111111:user/dev" {
			devFindings = append(devFindings, findings[i])
		}
	}
	require.Len(t, devFindings, 1,
		"expected exactly one merged finding from dev (two rules, one terminal)")

	f := devFindings[0]
	ruleTokens := evidenceRuleTokens(f)
	sort.Strings(ruleTokens)
	assert.Contains(t, ruleTokens, "update_assume_role_policy",
		"update_assume_role_policy must be in Evidence")
	assert.Contains(t, ruleTokens, "run_instances",
		"run_instances must be in Evidence")
	assert.GreaterOrEqual(t, f.Metrics["contributing_rules"], 2.0,
		"contributing_rules must count at least the two expected rules")
	assert.InDelta(t, 0.9, f.Metrics["min_confidence"], 1e-9,
		"min_confidence must be the lowest contributor (run_instances = 0.9)")
}

// evidenceRuleTokens returns the "rule:<name>" suffixes in a finding's
// Evidence list, stripped of the "rule:" prefix.
func evidenceRuleTokens(f Finding) []string {
	var out []string
	for _, ev := range f.Evidence {
		if after, ok := strings.CutPrefix(ev, "rule:"); ok {
			out = append(out, after)
		}
	}
	return out
}

// Ensure the dedup layer doesn't regress the existing
// TestIAMEscalationAnalyzer_NoEscalation, _DirectAssumeRole, _MultiHop,
// _CrossAccount, and _TopK tests — those assert path_length, hop_count,
// has_cross_account, and Evidence[0] semantics that dedupFindings
// intentionally leaves untouched. This test pins the non-regression
// contract explicitly so a future refactor can't silently break it.
func TestDedupFindings_PreservesSingleRuleFindings(t *testing.T) {
	src := "alice"
	tgt := "admin"
	path := escalationPath{
		Source: src,
		Target: tgt,
		Nodes:  []string{src, tgt},
		Edges: []iamInferredEdge{
			{FromID: src, ToID: tgt, Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy", Confidence: 1.0},
		},
	}
	inferred := map[string][]iamInferredEdge{
		src: {
			{FromID: src, ToID: tgt, Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy", Confidence: 1.0},
		},
	}
	findings := dedupFindings(t.Context(), Request{}, []escalationPath{path}, inferred)
	require.Len(t, findings, 1)
	f := findings[0]
	// Evidence[0] must still be the source (unchanged ordering).
	assert.Equal(t, src, f.Evidence[0])
	assert.InDelta(t, 1.0, f.Metrics["hop_count"], 1e-9)
	assert.InDelta(t, 2.0, f.Metrics["path_length"], 1e-9)
	assert.InDelta(t, 1.0, f.Metrics["min_confidence"], 1e-9)
	assert.InDelta(t, 1.0, f.Metrics["contributing_rules"], 1e-9)
}

// Sanity: the helper we just wrote must pick up on the "rule:" prefix
// correctly — one tiny compile-time check via use rather than a unit
// test for its own sake. Also keeps store imported for the fixture test.
var _ = kgtypes.GraphCloud
