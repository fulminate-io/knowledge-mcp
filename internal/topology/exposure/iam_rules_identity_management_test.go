// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_identity_management_test.go covers addUserToGroupRule. Cases:
//
//   - Admin group present → attacker self-promotes via attach_policy self-loop
//   - Non-admin group with members → impersonate edges to every member
//   - Non-admin group with no members → no edges
//   - Principal lacks iam:AddUserToGroup → 0 edges
//   - Two groups (one admin, one non-admin) → admin self-loop AND
//     impersonate edges to the non-admin group's members
//
// All tests follow the helper / fixture conventions of iam_fixture_test.go,
// iam_rules_test.go, and iam_rules_credential_access_test.go.

// TestAddUserToGroupRule_AdminGroup_Positive verifies that when the target
// group has an admin policy attached, the attacker self-promotes via an
// iamEdgeAttachPolicy self-loop (mirrors attachPolicyRule's edge shape).
func TestAddUserToGroupRule_AdminGroup_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"aug", `{"Statement":[{"Effect":"Allow","Action":"iam:AddUserToGroup","Resource":"*"}]}`)
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/admins", "admins")
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:group/admins")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := addUserToGroupRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, attacker.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeAttachPolicy, edges[0].Kind)
}

// TestAddUserToGroupRule_NonAdminGroup_Impersonate verifies that when the
// target group is non-admin, the attacker emits one impersonate edge per
// existing member of the group.
func TestAddUserToGroupRule_NonAdminGroup_Impersonate(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"aug", `{"Statement":[{"Effect":"Allow","Action":"iam:AddUserToGroup","Resource":"*"}]}`)
	m1 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m1", "m1")
	m2 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m2", "m2")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
	addInlinePolicy(t, fx, "arn:aws:iam::111111111111:group/devs", "ro",
		`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	addGroupMembership(t, fx, accountA, m1.Id, "arn:aws:iam::111111111111:group/devs")
	addGroupMembership(t, fx, accountA, m2.Id, "arn:aws:iam::111111111111:group/devs")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := addUserToGroupRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 2)

	got := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		assert.Equal(t, iamEdgeImpersonate, e.Kind)
		got[e.ToID] = true
	}
	assert.True(t, got[m1.Id], "expected impersonate edge to m1")
	assert.True(t, got[m2.Id], "expected impersonate edge to m2")
}

// TestAddUserToGroupRule_EmptyGroup_NoEdges verifies that an empty
// non-admin group produces no edges.
func TestAddUserToGroupRule_EmptyGroup_NoEdges(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"aug", `{"Statement":[{"Effect":"Allow","Action":"iam:AddUserToGroup","Resource":"*"}]}`)
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
	addInlinePolicy(t, fx, "arn:aws:iam::111111111111:group/devs", "ro",
		`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := addUserToGroupRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestAddUserToGroupRule_NoAction_Negative verifies that a principal
// without iam:AddUserToGroup emits no edges even when admin and non-admin
// groups with members are present.
func TestAddUserToGroupRule_NoAction_Negative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/alice", "alice",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	member := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/member", "member")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
	addGroupMembership(t, fx, accountA, member.Id, "arn:aws:iam::111111111111:group/devs")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/admins", "admins")
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:group/admins")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := addUserToGroupRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestAddUserToGroupRule_MultipleGroups verifies the combined behavior:
// an attacker with iam:AddUserToGroup and two groups in the account (one
// admin, one non-admin with members) emits one attach_policy self-loop AND
// one impersonate edge per non-admin member.
func TestAddUserToGroupRule_MultipleGroups(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"aug", `{"Statement":[{"Effect":"Allow","Action":"iam:AddUserToGroup","Resource":"*"}]}`)
	// Admin group.
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/admins", "admins")
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:group/admins")
	// Non-admin group with two members.
	m1 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m1", "m1")
	m2 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m2", "m2")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
	addInlinePolicy(t, fx, "arn:aws:iam::111111111111:group/devs", "ro",
		`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	addGroupMembership(t, fx, accountA, m1.Id, "arn:aws:iam::111111111111:group/devs")
	addGroupMembership(t, fx, accountA, m2.Id, "arn:aws:iam::111111111111:group/devs")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := addUserToGroupRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	// 1 attach_policy self-loop + 2 impersonate (m1, m2) = 3.
	require.Len(t, edges, 3)

	var sawSelfLoop bool
	impersonated := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		switch e.Kind {
		case iamEdgeAttachPolicy:
			assert.Equal(t, attacker.Id, e.ToID, "attach_policy must be a self-loop")
			sawSelfLoop = true
		case iamEdgeImpersonate:
			impersonated[e.ToID] = true
		default:
			t.Errorf("unexpected edge kind %q", e.Kind)
		}
	}
	assert.True(t, sawSelfLoop, "expected one attach_policy self-loop on attacker")
	assert.True(t, impersonated[m1.Id], "expected impersonate edge to m1")
	assert.True(t, impersonated[m2.Id], "expected impersonate edge to m2")
}
