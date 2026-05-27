// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_narrative_test.go covers buildPMapperNarrative — the
// PMapper-style sentence-per-hop Summary renderer used by
// buildEscalationFinding (Phase 9 Step 2).
//
// The narrative format is load-bearing for agent-facing output: every
// test case pins the exact shape of one escalation chain so future
// refactors that accidentally change phrasing or hop ordering fail loudly.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildPMapperNarrative_EmptyPath verifies that a zero-edge path
// produces an empty string rather than a dangling "reaching
// AdministratorAccess." clause. The escalation BFS can't actually produce
// a zero-edge path (admins are skipped as sources), but defensive
// handling keeps the helper safe for future callers.
func TestBuildPMapperNarrative_EmptyPath(t *testing.T) {
	got := buildPMapperNarrative(Request{}, escalationPath{})
	assert.Empty(t, got)
}

// TestBuildPMapperNarrative_NonEmptyAnyPath verifies the
// "returns non-empty string for any valid path" criterion of Step 2. We
// cover 5+ path shapes, one per table row.
func TestBuildPMapperNarrative_NonEmptyAnyPath(t *testing.T) {
	cases := []struct {
		name string
		path escalationPath
	}{
		{
			name: "AttachRolePolicy self-promotion",
			path: escalationPath{
				Source: "alice",
				Target: "alice",
				Nodes:  []string{"alice", "alice"},
				Edges: []iamInferredEdge{
					{FromID: "alice", ToID: "alice", Kind: iamEdgeAttachPolicy, RuleName: "attach_policy"},
				},
			},
		},
		{
			name: "login-profile chain",
			path: escalationPath{
				Source: "alice",
				Target: "bob",
				Nodes:  []string{"alice", "bob", "bob"},
				Edges: []iamInferredEdge{
					{FromID: "alice", ToID: "bob", Kind: iamEdgeImpersonate, RuleName: "create_login_profile"},
					{FromID: "bob", ToID: "bob", Kind: iamEdgeAttachPolicy, RuleName: "attach_policy"},
				},
			},
		},
		{
			name: "group → user → role",
			path: escalationPath{
				Source: "mallory",
				Target: "admin-role",
				Nodes:  []string{"mallory", "dev-user", "admin-role"},
				Edges: []iamInferredEdge{
					{FromID: "mallory", ToID: "dev-user", Kind: iamEdgeImpersonate, RuleName: "add_user_to_group"},
					{FromID: "dev-user", ToID: "admin-role", Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy"},
				},
			},
		},
		{
			name: "PassRole + Lambda update",
			path: escalationPath{
				Source: "dev",
				Target: "lambda-admin-role",
				Nodes:  []string{"dev", "lambda-admin-role"},
				Edges: []iamInferredEdge{
					{FromID: "dev", ToID: "lambda-admin-role", Kind: iamEdgeExecuteAs, RuleName: "update_function_configuration"},
				},
			},
		},
		{
			name: "cross-account assume-role",
			path: escalationPath{
				Source: "arn:aws:iam::222222222222:user/bob",
				Target: "arn:aws:iam::111111111111:role/admin",
				Nodes:  []string{"arn:aws:iam::222222222222:user/bob", "arn:aws:iam::111111111111:role/admin"},
				Edges: []iamInferredEdge{
					{FromID: "arn:aws:iam::222222222222:user/bob", ToID: "arn:aws:iam::111111111111:role/admin", Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy"},
				},
			},
		},
		{
			name: "three-hop impersonate → assume → execute_as",
			path: escalationPath{
				Source: "attacker",
				Target: "admin-role",
				Nodes:  []string{"attacker", "victim", "mid-role", "admin-role"},
				Edges: []iamInferredEdge{
					{FromID: "attacker", ToID: "victim", Kind: iamEdgeImpersonate, RuleName: "create_access_key"},
					{FromID: "victim", ToID: "mid-role", Kind: iamEdgeAssumeRole, RuleName: "assume_role_trust_policy"},
					{FromID: "mid-role", ToID: "admin-role", Kind: iamEdgeExecuteAs, RuleName: "pass_role_lambda"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPMapperNarrative(Request{}, tc.path)
			require.NotEmpty(t, got, "expected non-empty narrative for %s", tc.name)

			// One sentence per edge: the narrative joins clauses with ", then ",
			// so the number of ", then " joiners plus 1 must equal the edge count.
			nJoiners := strings.Count(got, ", then ")
			assert.Equal(t, len(tc.path.Edges)-1, nJoiners,
				"expected %d ', then ' separators, got %d in %q",
				len(tc.path.Edges)-1, nJoiners, got)

			// Last sentence must name the terminal admin state.
			assert.True(t, strings.HasSuffix(got, "reaching AdministratorAccess."),
				"narrative must end with 'reaching AdministratorAccess.', got %q", got)

			// Every edge's human verb phrase must appear in the narrative —
			// this is the "uses humanKindLabel as verb phrase" criterion.
			for _, e := range tc.path.Edges {
				assert.Contains(t, got, humanKindLabel(e.Kind),
					"narrative must include verb phrase for %s edge", e.Kind)
			}

			// Every source and target node ID must appear (name resolution
			// falls back to the raw ID when req.DB is nil).
			for _, n := range tc.path.Nodes {
				assert.Contains(t, got, n, "narrative must mention %s", n)
			}
		})
	}
}

// TestBuildPMapperNarrative_ExactLoginProfileChain pins the exact output
// shape for the canonical login-profile escalation (from the plan's
// example) to catch accidental phrasing drift.
func TestBuildPMapperNarrative_ExactLoginProfileChain(t *testing.T) {
	p := escalationPath{
		Source: "alice",
		Target: "bob",
		Nodes:  []string{"alice", "bob", "bob"},
		Edges: []iamInferredEdge{
			{FromID: "alice", ToID: "bob", Kind: iamEdgeImpersonate, RuleName: "create_login_profile"},
			{FromID: "bob", ToID: "bob", Kind: iamEdgeAttachPolicy, RuleName: "attach_policy"},
		},
	}
	want := "alice can impersonate bob, then bob can attach an admin-equivalent policy to bob, reaching AdministratorAccess."
	got := buildPMapperNarrative(Request{}, p)
	assert.Equal(t, want, got)
}
