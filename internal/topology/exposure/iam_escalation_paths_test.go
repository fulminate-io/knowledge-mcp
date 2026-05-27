// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_paths_test.go covers the pure BFS machinery in
// iam_escalation_paths.go with synthetic inferred-edge maps — no cloud
// fixture, no rule dispatch, no database. These tests pin the walker's
// state-key invariants (Phase 9.5 OQ-7), particularly:
//
//   1. Cross-account cycles terminate without runaway.
//   2. The visited set keys on (Account, ID) tuples, not IDs alone, so
//      a legitimate re-encounter of the same ARN in a different account
//      context would be explored — but since IAM ARNs are globally
//      unique this second property reduces to "no regression vs the
//      single-account walker".

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBFS_CrossAccountCycleTermination verifies that a tight A→B→A→B
// trust cycle does NOT produce a runaway BFS. The walker must visit
// each (account, principal) tuple at most once, exhaust the queue
// safely, and — because there is no admin in the cycle — return zero
// paths.
//
// Fixture shape (account:principal ARNs):
//
//	arn:aws:iam::A:role/a-role  --assume-->  arn:aws:iam::B:role/b-role
//	arn:aws:iam::B:role/b-role  --assume-->  arn:aws:iam::A:role/a-role
//
// admins set is empty. A cycle-blind BFS would enqueue these tuples
// forever; the visitKey-keyed walker terminates in 2 distinct visits.
func TestBFS_CrossAccountCycleTermination(t *testing.T) {
	aRole := "arn:aws:iam::111111111111:role/a-role"
	bRole := "arn:aws:iam::222222222222:role/b-role"
	inferred := map[string][]iamInferredEdge{
		aRole: {{
			FromID:  aRole,
			ToID:    bRole,
			Account: "111111111111",
			Kind:    iamEdgeAssumeRole,
			Reason:  "trust policy",
		}},
		bRole: {{
			FromID:  bRole,
			ToID:    aRole,
			Account: "222222222222",
			Kind:    iamEdgeAssumeRole,
			Reason:  "trust policy",
		}},
	}
	admins := map[string]bool{}

	paths := bfsToAdmin(
		newTestCtx(t),
		nil, // no native-edge lookup needed for pure-inferred walk
		inferred,
		admins,
		aRole,
		"111111111111",
		maxEscalationDepth,
	)
	// No admin reachable — BFS terminates with zero paths.
	assert.Empty(t, paths, "cycle with no admin must produce no paths")
}

// TestBFS_CrossAccountCycleWithAdminFinds verifies the companion case:
// a cross-account cycle that ALSO has an admin reachable from one
// branch produces exactly one shortest path and does not loop forever.
// Cycle: a-role in A <-> b-role in B; b-role additionally assumes an
// admin role in B. The BFS should find a-role → b-role → admin-b in
// two hops without getting stuck on the back-edge.
func TestBFS_CrossAccountCycleWithAdminFinds(t *testing.T) {
	aRole := "arn:aws:iam::111111111111:role/a-role"
	bRole := "arn:aws:iam::222222222222:role/b-role"
	adminB := "arn:aws:iam::222222222222:role/admin-b"
	inferred := map[string][]iamInferredEdge{
		aRole: {{FromID: aRole, ToID: bRole, Account: "111111111111", Kind: iamEdgeAssumeRole}},
		bRole: {
			{FromID: bRole, ToID: aRole, Account: "222222222222", Kind: iamEdgeAssumeRole},
			{FromID: bRole, ToID: adminB, Account: "222222222222", Kind: iamEdgeAssumeRole},
		},
	}
	admins := map[string]bool{adminB: true}

	paths := bfsToAdmin(
		newTestCtx(t),
		nil,
		inferred,
		admins,
		aRole,
		"111111111111",
		maxEscalationDepth,
	)
	require.Len(t, paths, 1, "one admin path expected despite back-edge cycle")
	p := paths[0]
	assert.Equal(t, aRole, p.Source)
	assert.Equal(t, adminB, p.Target)
	require.Len(t, p.Nodes, 3, "shortest path is aRole → bRole → adminB")
	assert.Equal(t, []string{aRole, bRole, adminB}, p.Nodes)
	require.Len(t, p.Accounts, 3)
	assert.Equal(t, "111111111111", p.Accounts[0])
	assert.Equal(t, "222222222222", p.Accounts[1])
	assert.Equal(t, "222222222222", p.Accounts[2])
}

// TestBFS_MaxDepthCap verifies the max-depth cap is enforced for
// cross-account walks. Fixture builds a chain longer than maxDepth
// (each hop alternating accounts); admin sits past the cap.
func TestBFS_MaxDepthCap(t *testing.T) {
	// Build an 8-hop chain a0→a1→...→a7 with admin at the end.
	// maxEscalationDepth (6) means we never reach a7.
	const n = 8
	nodes := make([]string, n+1)
	for i := 0; i <= n; i++ {
		acct := "111111111111"
		if i%2 == 1 {
			acct = "222222222222"
		}
		nodes[i] = "arn:aws:iam::" + acct + ":role/hop" + string(rune('0'+i))
	}
	inferred := map[string][]iamInferredEdge{}
	for i := range n {
		inferred[nodes[i]] = []iamInferredEdge{{
			FromID: nodes[i],
			ToID:   nodes[i+1],
			Kind:   iamEdgeAssumeRole,
		}}
	}
	admins := map[string]bool{nodes[n]: true}

	paths := bfsToAdmin(
		newTestCtx(t),
		nil,
		inferred,
		admins,
		nodes[0],
		"111111111111",
		maxEscalationDepth,
	)
	assert.Empty(t, paths, "admin past maxDepth must not be found")
}

// TestAccountFromARN covers the ARN-account-segment parser used by the
// walker to determine successor account context. Service principal and
// malformed inputs must return the empty string.
func TestAccountFromARN(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"arn:aws:iam::111111111111:role/admin", "111111111111"},
		{"arn:aws:iam::222222222222:user/alice", "222222222222"},
		{"arn:aws:iam::aws:policy/AdministratorAccess", ""},
		{"not-an-arn", ""},
		{"", ""},
		{"arn:aws:s3:::my-bucket", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, accountFromARN(tc.id), "accountFromARN(%q)", tc.id)
	}
}
