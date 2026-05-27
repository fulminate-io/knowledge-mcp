// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_credential_access_test.go covers createLoginProfileRule and
// updateLoginProfileRule. Positive, negative, multi-user, and skip-self
// cases for each rule. Test fixtures come from iam_fixture_test.go.
//
// These tests intentionally mirror the shape of
// TestCreateAccessKeyRule_Positive / _Negative in iam_rules_test.go so
// the credential-theft rules stay consistent with the v1.1 convention:
// the attacker's own user is never a target of its own impersonation
// edge.

// TestCreateLoginProfileRule_Positive verifies a principal that can call
// iam:CreateLoginProfile emits one impersonate edge to every other
// iam-user in the account.
func TestCreateLoginProfileRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/attacker", "attacker",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)
	victim := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, victim.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeImpersonate, edges[0].Kind)
}

// TestCreateLoginProfileRule_NoAction verifies a principal lacking
// iam:CreateLoginProfile emits no edges even when users are present.
func TestCreateLoginProfileRule_NoAction(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/bob", "bob")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestCreateLoginProfileRule_MultipleUsers verifies one principal with the
// action emits one edge per other iam-user in the account. Three victims
// plus one attacker → three edges (attacker skips self).
func TestCreateLoginProfileRule_MultipleUsers(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/attacker", "attacker",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)
	v1 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim1", "victim1")
	v2 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim2", "victim2")
	v3 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim3", "victim3")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 3)

	// Every edge must originate from the attacker and land on a victim
	// (not on the attacker itself).
	victims := map[string]bool{v1.Id: false, v2.Id: false, v3.Id: false}
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		assert.Equal(t, iamEdgeImpersonate, e.Kind)
		_, ok := victims[e.ToID]
		assert.True(t, ok, "edge target %q not in victim set", e.ToID)
		victims[e.ToID] = true
	}
	for id, hit := range victims {
		assert.True(t, hit, "victim %q never received an edge", id)
	}
}

// TestCreateLoginProfileRule_SkipSelf verifies the attacker does not emit
// a self-impersonation edge when it is itself an iam-user in the same
// account. Mirrors the createAccessKeyRule skip-self convention.
func TestCreateLoginProfileRule_SkipSelf(t *testing.T) {
	fx := newCloudFixture(t)
	// Only the attacker exists — there are no other users to target.
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/attacker", "attacker",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges, "attacker must not emit a self-impersonation edge")
}

// TestUpdateLoginProfileRule_Positive verifies a principal that can call
// iam:UpdateLoginProfile emits one impersonate edge to every other
// iam-user in the account.
func TestUpdateLoginProfileRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/attacker", "attacker",
		"ulp", `{"Statement":[{"Effect":"Allow","Action":"iam:UpdateLoginProfile","Resource":"*"}]}`)
	victim := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, victim.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeImpersonate, edges[0].Kind)
}

// TestUpdateLoginProfileRule_NoAction verifies a principal lacking
// iam:UpdateLoginProfile emits no edges even when users are present.
func TestUpdateLoginProfileRule_NoAction(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/bob", "bob")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestUpdateLoginProfileRule_ActionIsolation verifies a principal with
// iam:CreateLoginProfile but NOT iam:UpdateLoginProfile does not trigger
// updateLoginProfileRule. Makes sure the two rules don't cross-trigger.
func TestUpdateLoginProfileRule_ActionIsolation(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"lp", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateLoginProfileRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges, "updateLoginProfileRule must not fire on iam:CreateLoginProfile")
}
