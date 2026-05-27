// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_lambda_update_test.go covers the two Phase 7 Lambda update rules
// added in iam_rules_lambda_update.go:
//
//	updateFunctionCodeRule          — per-function execution-role escalation
//	updateFunctionConfigurationRule — multi-action PassRole reuse
//
// Each rule has positive cases plus negative cases that pin the boundary
// behavior. TestIAMRulesRegistered (in iam_rules_test.go) pins the
// registration set itself; this file does not duplicate that.
//
// updateFunctionCodeRule is the more interesting shape: its target set is
// per-function (each function's execution role) rather than a cross-product
// of (callers, roles trusting a service). The five UpdateFunctionCode tests
// pin the four corners of that grid: rule fires, rule does not fire without
// the action, rule does not fire when no functions exist, multi-function
// fan-out, and the function-without-role edge case.
//
// updateFunctionConfigurationRule reuses passRoleServiceMultiActionRule, so
// the tests focus on the new combination (UpdateFunctionConfiguration AND
// PassRole) and the partial-actions negative branch.

const (
	lambdaUpdateAccount = accountA
	lambdaUpdateUserARN = "arn:aws:iam::111111111111:user/lambda-attacker"
	lambdaUpdateRoleARN = "arn:aws:iam::111111111111:role/lambda-exec"
	lambdaUpdateFnARN   = "arn:aws:iam::111111111111:function/target-fn"
)

// lambdaTrustPolicyJSON is the canonical lambda.amazonaws.com trust policy
// used by every Lambda function execution role in this file.
const lambdaTrustPolicyJSON = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// TestUpdateFunctionCodeRule_Positive verifies that a principal with
// lambda:UpdateFunctionCode plus an existing function-with-role yields one
// execute_as edge from the principal to the function's execution role.
func TestUpdateFunctionCodeRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)

	user := addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"upd", `{"Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionCode","Resource":"*"}]}`)
	role := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount, lambdaUpdateRoleARN, "lambda-exec",
		lambdaTrustPolicyJSON)
	addLambdaFunction(t, fx, lambdaUpdateAccount, lambdaUpdateFnARN, "target-fn", role.Id)

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	require.Len(t, rctx.Functions, 1, "fixture should expose one lambda-function")

	edges, err := updateFunctionCodeRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, role.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeExecuteAs, edges[0].Kind)
}

// TestUpdateFunctionCodeRule_NoAction_Negative verifies the rule does not
// fire when no principal allows lambda:UpdateFunctionCode, even if functions
// with execution roles exist.
func TestUpdateFunctionCodeRule_NoAction_Negative(t *testing.T) {
	fx := newCloudFixture(t)

	addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	role := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount, lambdaUpdateRoleARN, "lambda-exec",
		lambdaTrustPolicyJSON)
	addLambdaFunction(t, fx, lambdaUpdateAccount, lambdaUpdateFnARN, "target-fn", role.Id)

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	edges, err := updateFunctionCodeRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges, "rule must not fire without lambda:UpdateFunctionCode")
}

// TestUpdateFunctionCodeRule_NoFunctions verifies the rule does not fire
// when the action is granted but no lambda-function nodes exist. The
// per-function shape requires at least one function to define a target.
func TestUpdateFunctionCodeRule_NoFunctions(t *testing.T) {
	fx := newCloudFixture(t)

	addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"upd", `{"Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionCode","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, lambdaUpdateAccount, lambdaUpdateRoleARN, "lambda-exec",
		lambdaTrustPolicyJSON)

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	require.Empty(t, rctx.Functions, "fixture should have zero lambda-function nodes")

	edges, err := updateFunctionCodeRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges, "rule must not fire when no lambda-functions exist")
}

// TestUpdateFunctionCodeRule_MultipleFunctions verifies that N functions with
// distinct execution roles produce N edges from the same caller. The fan-out
// is per-function rather than per-role so the BFS narrative can attribute
// each escalation to a specific function ID.
func TestUpdateFunctionCodeRule_MultipleFunctions(t *testing.T) {
	fx := newCloudFixture(t)

	user := addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"upd", `{"Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionCode","Resource":"*"}]}`)

	roleA := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:role/exec-a", "exec-a", lambdaTrustPolicyJSON)
	roleB := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:role/exec-b", "exec-b", lambdaTrustPolicyJSON)
	roleC := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:role/exec-c", "exec-c", lambdaTrustPolicyJSON)

	addLambdaFunction(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:function/fn-a", "fn-a", roleA.Id)
	addLambdaFunction(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:function/fn-b", "fn-b", roleB.Id)
	addLambdaFunction(t, fx, lambdaUpdateAccount,
		"arn:aws:iam::111111111111:function/fn-c", "fn-c", roleC.Id)

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	require.Len(t, rctx.Functions, 3)

	edges, err := updateFunctionCodeRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 3, "expected one edge per function")

	// Every edge originates from the same caller and lands on a distinct role.
	roleHits := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, user.Id, e.FromID)
		assert.Equal(t, iamEdgeExecuteAs, e.Kind)
		roleHits[e.ToID] = true
	}
	assert.True(t, roleHits[roleA.Id])
	assert.True(t, roleHits[roleB.Id])
	assert.True(t, roleHits[roleC.Id])
}

// TestUpdateFunctionCodeRule_FunctionWithoutRole verifies that a lambda
// function with no EdgeAssumesRole link is silently skipped. The rule cannot
// describe an escalation against a function that has no role to inherit, so
// it must not panic and must not emit a self-loop or empty-target edge.
func TestUpdateFunctionCodeRule_FunctionWithoutRole(t *testing.T) {
	fx := newCloudFixture(t)

	addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"upd", `{"Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionCode","Resource":"*"}]}`)
	// Function with empty executionRoleARN — no EdgeAssumesRole link.
	addLambdaFunction(t, fx, lambdaUpdateAccount, lambdaUpdateFnARN, "target-fn", "")

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	require.Len(t, rctx.Functions, 1)

	edges, err := updateFunctionCodeRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges, "function without execution role must yield zero edges")
}

// TestUpdateFunctionConfigurationRule_Positive verifies the rule fires when
// a principal allows BOTH lambda:UpdateFunctionConfiguration and
// iam:PassRole and a target role trusts lambda.amazonaws.com. The target
// role is enumerated from rctx.Roles via the multi-action helper, not from
// rctx.Functions — UpdateFunctionConfiguration swaps in any lambda-trusting
// role the attacker can pass.
func TestUpdateFunctionConfigurationRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)

	user := addIAMUserWithInline(t, fx, lambdaUpdateAccount, lambdaUpdateUserARN, "lambda-attacker",
		"upd", `{"Statement":[{"Effect":"Allow","Action":["lambda:UpdateFunctionConfiguration","iam:PassRole"],"Resource":"*"}]}`)
	role := addIAMRoleWithTrust(t, fx, lambdaUpdateAccount, lambdaUpdateRoleARN, "lambda-exec",
		lambdaTrustPolicyJSON)

	rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
	edges, err := updateFunctionConfigurationRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, role.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeExecuteAs, edges[0].Kind)
}

// TestUpdateFunctionConfigurationRule_PartialActions_Negative verifies the
// rule does NOT fire when the principal allows only one of the two required
// actions. Mirrors TestEcsRunTaskRule_PartialActionsNegative — exercises the
// AND short-circuit in passRoleServiceMultiActionRule for this rule's
// specific action pair.
func TestUpdateFunctionConfigurationRule_PartialActions_Negative(t *testing.T) {
	cases := []struct {
		name      string
		inlineDoc string
	}{
		{
			name:      "only_update_configuration",
			inlineDoc: `{"Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionConfiguration","Resource":"*"}]}`,
		},
		{
			name:      "only_pass_role",
			inlineDoc: `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCloudFixture(t)
			addIAMUserWithInline(t, fx, lambdaUpdateAccount,
				"arn:aws:iam::111111111111:user/partial", "partial", "p", tc.inlineDoc)
			addIAMRoleWithTrust(t, fx, lambdaUpdateAccount, lambdaUpdateRoleARN, "lambda-exec",
				lambdaTrustPolicyJSON)

			rctx := newTestRuleContext(t, fx, lambdaUpdateAccount)
			edges, err := updateFunctionConfigurationRule(newTestCtx(t), rctx)
			require.NoError(t, err)
			assert.Empty(t, edges,
				"update_function_configuration must require BOTH actions; %s should yield no edges", tc.name)
		})
	}
}
