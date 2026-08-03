// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_eval_ctx_test.go covers the condition-aware evaluator
// EvaluateActionWithContext / AllowsActionWithContext. Each test targets
// one axis of the evaluator:
//
//   - Allow + satisfied condition  → Allow
//   - Allow + failed condition     → NoMatch (statement skipped)
//   - Deny  + satisfied condition  → ExplicitDeny (overrides broad Allow)
//   - Deny  + failed condition     → Allow (Deny skipped, broader Allow wins)
//   - Bool operator (MFA present)
//   - Empty condition vacuously true
//   - Backward compat: old EvaluateAction still permissive w.r.t. conditions
//   - Nil receiver safety
//
// Split from iam_parser_test.go under the repo's 500-line hard cap.
// The shared test helpers (require/assert, ParseIAMPolicy) live
// in iam_parser.go / iam_parser_conditions.go respectively.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEvaluateActionWithContext_ConditionMatches_GrantsAction verifies that
// an Allow statement whose Condition is satisfied by the supplied context
// yields Allow. The condition constrains the principal ARN via StringEquals.
func TestEvaluateActionWithContext_ConditionMatches_GrantsAction(t *testing.T) {
	doc := `{"Statement":[{
		"Effect":"Allow",
		"Action":"s3:GetObject",
		"Resource":"*",
		"Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/alice"}}
	}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	cctx := ConditionContext{PrincipalArn: "arn:aws:iam::123456789012:user/alice"}
	assert.Equal(t, Allow, p.EvaluateActionWithContext("s3:GetObject", "*", cctx))
	assert.True(t, p.AllowsActionWithContext("s3:GetObject", "*", cctx))
}

// TestEvaluateActionWithContext_ConditionFails_NoMatch verifies that an
// Allow statement whose Condition is NOT satisfied is skipped entirely,
// yielding NoMatch rather than Allow.
func TestEvaluateActionWithContext_ConditionFails_NoMatch(t *testing.T) {
	doc := `{"Statement":[{
		"Effect":"Allow",
		"Action":"s3:GetObject",
		"Resource":"*",
		"Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/alice"}}
	}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	cctx := ConditionContext{PrincipalArn: "arn:aws:iam::123456789012:user/bob"}
	assert.Equal(t, NoMatch, p.EvaluateActionWithContext("s3:GetObject", "*", cctx))
	assert.False(t, p.AllowsActionWithContext("s3:GetObject", "*", cctx))
}

// TestEvaluateActionWithContext_DenyWithConditionMatches_ExplicitDeny verifies
// that a Deny statement whose Condition matches still short-circuits to
// ExplicitDeny, overriding any Allow (condition-aware explicit deny).
func TestEvaluateActionWithContext_DenyWithConditionMatches_ExplicitDeny(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{
			"Effect":"Deny",
			"Action":"s3:DeleteBucket",
			"Resource":"*",
			"Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/alice"}}
		}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	cctx := ConditionContext{PrincipalArn: "arn:aws:iam::123456789012:user/alice"}
	assert.Equal(t, ExplicitDeny,
		p.EvaluateActionWithContext("s3:DeleteBucket", "*", cctx))
}

// TestEvaluateActionWithContext_DenyWithConditionFails_AllowStands verifies
// the inverse of the previous test: when a Deny statement's Condition does
// NOT match, the Deny is skipped, so a broader Allow still grants the
// action. This is the critical semantic that lets conditional denies be
// narrowly scoped.
func TestEvaluateActionWithContext_DenyWithConditionFails_AllowStands(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{
			"Effect":"Deny",
			"Action":"s3:DeleteBucket",
			"Resource":"*",
			"Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/alice"}}
		}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	cctx := ConditionContext{PrincipalArn: "arn:aws:iam::123456789012:user/bob"}
	// Bob is not Alice, so the Deny's condition fails and the Allow
	// statement (which has no condition) governs.
	assert.Equal(t, Allow, p.EvaluateActionWithContext("s3:DeleteBucket", "*", cctx))
}

// TestEvaluateActionWithContext_MFARequired_MatchesWhenMFA verifies the
// Bool operator path — a statement requiring aws:MultiFactorAuthPresent
// is allowed only when the context reports MFA present.
func TestEvaluateActionWithContext_MFARequired_MatchesWhenMFA(t *testing.T) {
	doc := `{"Statement":[{
		"Effect":"Allow",
		"Action":"iam:DeleteUser",
		"Resource":"*",
		"Condition":{"Bool":{"aws:MultiFactorAuthPresent":"true"}}
	}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	withMFA := ConditionContext{MFAPresent: true}
	withoutMFA := ConditionContext{MFAPresent: false}

	assert.Equal(t, Allow,
		p.EvaluateActionWithContext("iam:DeleteUser", "*", withMFA),
		"MFA-present context should satisfy Bool aws:MultiFactorAuthPresent=true")
	assert.Equal(t, NoMatch,
		p.EvaluateActionWithContext("iam:DeleteUser", "*", withoutMFA),
		"MFA-absent context should skip the Allow statement")
}

// TestEvaluateActionWithContext_NoCondition_BehavesLikeEvaluateAction verifies
// that a statement with NO condition block behaves identically under the
// context-aware evaluator as under the permissive evaluator. Empty/absent
// Condition is vacuously satisfied.
func TestEvaluateActionWithContext_NoCondition_BehavesLikeEvaluateAction(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"*"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Empty context — with no conditions on the statements, outcome is
	// identical to EvaluateAction.
	cctx := ConditionContext{}
	assert.Equal(t, Allow,
		p.EvaluateActionWithContext("s3:GetObject", "*", cctx))
	assert.Equal(t, ExplicitDeny,
		p.EvaluateActionWithContext("s3:DeleteBucket", "*", cctx))
	assert.Equal(t, NoMatch,
		p.EvaluateActionWithContext("iam:CreateUser", "*", cctx))
}

// TestEvaluateAction_Unchanged_BackCompat verifies that the condition-ignoring
// EvaluateAction retains its v1.1 permissive semantic — a statement with a
// Condition block still matches even when no ConditionContext exists. This
// locks in backward compatibility for the rule files that call EvaluateAction
// without threading a context through.
func TestEvaluateAction_Unchanged_BackCompat(t *testing.T) {
	doc := `{"Statement":[{
		"Effect":"Allow",
		"Action":"s3:GetObject",
		"Resource":"*",
		"Condition":{"StringEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/alice"}}
	}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// No context threaded — EvaluateAction must still return Allow,
	// preserving v1.1 behavior (conditions existed but did not gate).
	assert.Equal(t, Allow, p.EvaluateAction("s3:GetObject", "*"))
	assert.True(t, p.AllowsAction("s3:GetObject", "*"))
}

// TestEvaluateActionWithContext_NilPolicy_Safe verifies the nil-receiver
// guard is preserved for the context-aware entry points.
func TestEvaluateActionWithContext_NilPolicy_Safe(t *testing.T) {
	var p *IAMPolicy
	assert.Equal(t, NoMatch,
		p.EvaluateActionWithContext("s3:GetObject", "*", ConditionContext{}))
	assert.False(t,
		p.AllowsActionWithContext("s3:GetObject", "*", ConditionContext{}))
}
