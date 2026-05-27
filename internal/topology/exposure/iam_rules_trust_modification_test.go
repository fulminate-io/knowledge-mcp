// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_trust_modification_test.go covers updateAssumeRolePolicyRule.
// The rule emits one iamEdgeExecuteAs edge per (principal, role) pair where
// the principal's effective policy set allows iam:UpdateAssumeRolePolicy on
// the role's ARN. Resource-pattern semantics (Resource:"*", exact ARN, ARN
// prefix wildcard) are delegated to IAMPolicy.AllowsAction so each test
// targets a different shape of resource pattern to confirm the wiring.

const updateAssumeRoleAction = "iam:UpdateAssumeRolePolicy"

// trustyTrustPolicy is a benign trust policy that lets the resulting role
// node parse cleanly without affecting any rule under test. The trust-policy
// content is not consulted by updateAssumeRolePolicyRule (the rule reads only
// principal policies and role.Id), so the body can be a no-op stub.
const trustyTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// inlinePolicyDoc returns a one-statement Allow policy for the given action
// and resource. Used by every test in this file to construct attacker
// principals with varying scopes.
func inlinePolicyDoc(action, resource string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"` +
		action + `","Resource":"` + resource + `"}]}`
}

// TestUpdateAssumeRolePolicyRule_Wildcard verifies a principal with
// Resource: "*" gets one edge per role in the account.
func TestUpdateAssumeRolePolicyRule_Wildcard(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction, "*"))
	role1 := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/r1", "r1", trustyTrustPolicy)
	role2 := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/r2", "r2", trustyTrustPolicy)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 2)

	got := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		assert.Equal(t, iamEdgeExecuteAs, e.Kind)
		got[e.ToID] = true
	}
	assert.True(t, got[role1.Id])
	assert.True(t, got[role2.Id])
}

// TestUpdateAssumeRolePolicyRule_SpecificResource verifies a principal with
// Resource: <exact role ARN> gets exactly one edge to that role.
func TestUpdateAssumeRolePolicyRule_SpecificResource(t *testing.T) {
	fx := newCloudFixture(t)
	target := "arn:aws:iam::111111111111:role/target-role"
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction, target))
	addIAMRoleWithTrust(t, fx, accountA, target, "target-role", trustyTrustPolicy)
	addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/other", "other", trustyTrustPolicy)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, target, edges[0].ToID)
	assert.Equal(t, iamEdgeExecuteAs, edges[0].Kind)
}

// TestUpdateAssumeRolePolicyRule_NoAction_Negative verifies that a principal
// without the action emits zero edges, even when roles exist.
func TestUpdateAssumeRolePolicyRule_NoAction_Negative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/alice", "alice",
		"ro", inlinePolicyDoc("s3:GetObject", "*"))
	addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/admin", "admin", trustyTrustPolicy)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestUpdateAssumeRolePolicyRule_NoRoles_Empty verifies the rule short-circuits
// when no roles exist in the account, even if a principal allows the action.
func TestUpdateAssumeRolePolicyRule_NoRoles_Empty(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction, "*"))

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestUpdateAssumeRolePolicyRule_MultiplePrincipals_MultipleRoles verifies the
// N principals × M roles cross-product. Two attackers with wildcard scope
// against three roles must yield 2 × 3 = 6 edges.
func TestUpdateAssumeRolePolicyRule_MultiplePrincipals_MultipleRoles(t *testing.T) {
	fx := newCloudFixture(t)
	a1 := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker1", "attacker1",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction, "*"))
	a2 := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker2", "attacker2",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction, "*"))
	r1 := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/r1", "r1", trustyTrustPolicy)
	r2 := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/r2", "r2", trustyTrustPolicy)
	r3 := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/r3", "r3", trustyTrustPolicy)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 6)

	pairs := make(map[string]bool)
	for _, e := range edges {
		assert.Equal(t, iamEdgeExecuteAs, e.Kind)
		pairs[e.FromID+"|"+e.ToID] = true
	}
	for _, principal := range []string{a1.Id, a2.Id} {
		for _, role := range []string{r1.Id, r2.Id, r3.Id} {
			assert.True(t, pairs[principal+"|"+role],
				"missing pair %s -> %s", principal, role)
		}
	}
}

// TestUpdateAssumeRolePolicyRule_ArnPrefixPattern verifies AWS-style prefix
// wildcard patterns: arn:aws:iam::123:role/env-* matches env-dev and
// env-prod but does NOT match prod (no env- prefix).
func TestUpdateAssumeRolePolicyRule_ArnPrefixPattern(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"hijack", inlinePolicyDoc(updateAssumeRoleAction,
			"arn:aws:iam::111111111111:role/env-*"))
	envDev := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/env-dev", "env-dev", trustyTrustPolicy)
	envProd := addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/env-prod", "env-prod", trustyTrustPolicy)
	addIAMRoleWithTrust(t, fx, accountA,
		"arn:aws:iam::111111111111:role/prod", "prod", trustyTrustPolicy)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := updateAssumeRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 2)

	got := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		assert.Equal(t, iamEdgeExecuteAs, e.Kind)
		got[e.ToID] = true
	}
	assert.True(t, got[envDev.Id], "env-dev must be matched by env-* pattern")
	assert.True(t, got[envProd.Id], "env-prod must be matched by env-* pattern")
}

// TestUpdateAssumeRolePolicyRule_RegistrationPin verifies the rule is
// registered under the canonical name. The full want-list pin lives in
// TestIAMRulesRegistered; this test just confirms the rule itself is wired.
func TestUpdateAssumeRolePolicyRule_RegistrationPin(t *testing.T) {
	rule, ok := lookupIAMRule("update_assume_role_policy")
	require.True(t, ok, "update_assume_role_policy must be registered")
	require.NotNil(t, rule)
}
