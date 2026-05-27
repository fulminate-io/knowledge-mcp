// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_parser_test.go covers the JSON shape variability and wildcard
// semantics of the IAM policy parser. Each test focuses on one feature
// (string-vs-slice fields, NotAction inversion, condition presence, etc.)
// so a failure pinpoints the exact rule the parser broke.

// TestParseIAMPolicy_StringAction verifies the single-string Action shape
// is normalized into a one-element slice.
func TestParseIAMPolicy_StringAction(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	require.Len(t, p.Statements, 1)
	assert.Equal(t, []string{"s3:GetObject"}, p.Statements[0].Action)
	assert.Equal(t, []string{"*"}, p.Statements[0].Resource)
	assert.Equal(t, "Allow", p.Statements[0].Effect)
}

// TestParseIAMPolicy_SliceAction verifies the array Action shape.
func TestParseIAMPolicy_SliceAction(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":["arn:aws:s3:::a","arn:aws:s3:::b"]}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	require.Len(t, p.Statements, 1)
	assert.Equal(t, []string{"s3:GetObject", "s3:PutObject"}, p.Statements[0].Action)
	assert.Equal(t, []string{"arn:aws:s3:::a", "arn:aws:s3:::b"}, p.Statements[0].Resource)
}

// TestParseIAMPolicy_SingleStatement verifies the Statement-as-object
// shape (not array) is normalized into a one-element slice.
func TestParseIAMPolicy_SingleStatement(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"*"}}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	require.Len(t, p.Statements, 1)
	assert.Equal(t, "Allow", p.Statements[0].Effect)
}

// TestParseIAMPolicy_Empty rejects empty input rather than returning a
// zero-value pointer that callers would mistake for a valid policy.
func TestParseIAMPolicy_Empty(t *testing.T) {
	_, err := ParseIAMPolicy(nil)
	require.Error(t, err)
	_, err = ParseIAMPolicy([]byte(""))
	require.Error(t, err)
}

// TestParseIAMPolicy_InvalidJSON returns an error.
func TestParseIAMPolicy_InvalidJSON(t *testing.T) {
	_, err := ParseIAMPolicy([]byte("not json"))
	require.Error(t, err)
}

// TestAllowsAction_ExactMatch verifies exact action matching.
func TestAllowsAction_ExactMatch(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("iam:PassRole", "*"))
	assert.False(t, p.AllowsAction("iam:CreateAccessKey", "*"))
}

// TestAllowsAction_WildcardAll verifies "*" matches everything.
func TestAllowsAction_WildcardAll(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("iam:CreateUser", "*"))
	assert.True(t, p.AllowsAction("s3:DeleteBucket", "arn:aws:s3:::x"))
}

// TestAllowsAction_PrefixWildcard verifies "iam:*" matches every iam: action.
func TestAllowsAction_PrefixWildcard(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("iam:CreateUser", "*"))
	assert.True(t, p.AllowsAction("iam:DeleteAccessKey", "*"))
	assert.False(t, p.AllowsAction("s3:GetObject", "*"))
}

// TestAllowsAction_SuffixWildcard verifies "iam:Attach*" matches the
// expected iam:AttachUserPolicy / iam:AttachRolePolicy actions.
func TestAllowsAction_SuffixWildcard(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"iam:Attach*","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("iam:AttachUserPolicy", "*"))
	assert.True(t, p.AllowsAction("iam:AttachRolePolicy", "*"))
	assert.False(t, p.AllowsAction("iam:DetachUserPolicy", "*"))
}

// TestAllowsAction_NotAction verifies NotAction inverts the match.
func TestAllowsAction_NotAction(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","NotAction":"iam:DeleteUser","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("iam:CreateUser", "*"))
	assert.False(t, p.AllowsAction("iam:DeleteUser", "*"))
}

// TestAllowsAction_DenyEffect verifies Deny statements never match.
func TestAllowsAction_DenyEffect(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.False(t, p.AllowsAction("iam:CreateUser", "*"))
}

// TestAllowsAction_ResourcePrefix verifies resource prefix wildcards.
func TestAllowsAction_ResourcePrefix(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::mybucket/*"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.AllowsAction("s3:GetObject", "arn:aws:s3:::mybucket/key1"))
	assert.False(t, p.AllowsAction("s3:GetObject", "arn:aws:s3:::other/key1"))
}

// TestIsEffectiveAdmin_Strict verifies the strict definition: Allow + * + *.
func TestIsEffectiveAdmin_Strict(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{
			name: "wildcard everything",
			doc:  `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
			want: true,
		},
		{
			name: "wildcard action only",
			doc:  `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"arn:aws:s3:::x"}]}`,
			want: false,
		},
		{
			name: "wildcard resource only",
			doc:  `{"Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"}]}`,
			want: false,
		},
		{
			name: "deny effect",
			doc:  `{"Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`,
			want: false,
		},
		{
			name: "PowerUserAccess shape (NotAction iam:*) — must NOT count",
			doc:  `{"Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseIAMPolicy([]byte(tc.doc))
			require.NoError(t, err)
			assert.Equal(t, tc.want, p.IsEffectiveAdmin())
		})
	}
}

// TestTrustPrincipals_AWSAccount verifies extraction of AWS principal ARNs.
func TestTrustPrincipals_AWSAccount(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":"sts:AssumeRole"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.Equal(t, []string{"arn:aws:iam::222222222222:root"}, p.TrustPrincipals())
}

// TestTrustPrincipals_AWSSlice verifies the array shape.
func TestTrustPrincipals_AWSSlice(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111:user/u1","arn:aws:iam::222:role/r1"]},"Action":"sts:AssumeRole"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"arn:aws:iam::111:user/u1", "arn:aws:iam::222:role/r1"}, p.TrustPrincipals())
}

// TestTrustPrincipals_StarAll verifies the "*" Principal becomes All=true.
func TestTrustPrincipals_StarAll(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"sts:AssumeRole"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, p.TrustPrincipals())
}

// TestServicePrincipals_Lambda verifies Service principal extraction.
func TestServicePrincipals_Lambda(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.Equal(t, []string{"lambda.amazonaws.com"}, p.ServicePrincipals())
}

// TestHasCondition verifies condition presence detection.
func TestHasCondition(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:username":"alice"}}}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.True(t, p.HasCondition())

	doc2 := `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	p2, err := ParseIAMPolicy([]byte(doc2))
	require.NoError(t, err)
	assert.False(t, p2.HasCondition())
}

// TestNilPolicy_Safe verifies all helpers tolerate a nil receiver.
func TestNilPolicy_Safe(t *testing.T) {
	var p *IAMPolicy
	assert.False(t, p.AllowsAction("*", "*"))
	assert.Equal(t, NoMatch, p.EvaluateAction("*", "*"))
	assert.False(t, p.ExplicitlyDenies("*", "*"))
	assert.False(t, p.IsEffectiveAdmin())
	assert.Nil(t, p.TrustPrincipals())
	assert.Nil(t, p.ServicePrincipals())
	assert.False(t, p.HasCondition())
}

// TestAllowsAction_ExplicitDenyOverridesAllow verifies the core AWS semantic:
// an explicit Deny statement blocks an Allow even when the Allow is broader.
// Policy allows every s3: action, but explicitly denies s3:GetObject — so
// AllowsAction("s3:GetObject", *) must return false even though a wildcard
// Allow matches.
func TestAllowsAction_ExplicitDenyOverridesAllow(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Denied action: Allow matches, Deny also matches → ExplicitDeny wins.
	assert.False(t, p.AllowsAction("s3:GetObject", "*"),
		"explicit Deny must override wildcard Allow")
	assert.Equal(t, ExplicitDeny, p.EvaluateAction("s3:GetObject", "*"))
	assert.True(t, p.ExplicitlyDenies("s3:GetObject", "*"))

	// Non-denied action: only Allow matches → Allow.
	assert.True(t, p.AllowsAction("s3:PutObject", "*"),
		"non-denied action should still be allowed by the wildcard Allow")
	assert.Equal(t, Allow, p.EvaluateAction("s3:PutObject", "*"))
	assert.False(t, p.ExplicitlyDenies("s3:PutObject", "*"))
}

// TestAllowsAction_DenyWithSpecificResource verifies that explicit Deny is
// scoped by the Deny statement's resource pattern. A Deny on a specific
// bucket path must NOT block Allow on unrelated resources.
func TestAllowsAction_DenyWithSpecificResource(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/secret/*"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// In the denied resource prefix: ExplicitDeny.
	assert.False(t, p.AllowsAction("s3:GetObject", "arn:aws:s3:::bucket/secret/keys.txt"))
	assert.Equal(t, ExplicitDeny,
		p.EvaluateAction("s3:GetObject", "arn:aws:s3:::bucket/secret/keys.txt"))

	// Outside the denied resource prefix: Allow stands.
	assert.True(t, p.AllowsAction("s3:GetObject", "arn:aws:s3:::bucket/public/doc.txt"))
	assert.Equal(t, Allow,
		p.EvaluateAction("s3:GetObject", "arn:aws:s3:::bucket/public/doc.txt"))

	// Action not under the Deny clause: Allow stands everywhere.
	assert.True(t, p.AllowsAction("s3:PutObject", "arn:aws:s3:::bucket/secret/keys.txt"))
}

// TestAllowsAction_MultipleStatementsMixed exercises a complex policy with
// several Allow and Deny statements covering overlapping action/resource
// surfaces. Each assertion pins one (action, resource) combination to its
// expected final decision.
func TestAllowsAction_MultipleStatementsMixed(t *testing.T) {
	doc := `{"Statement":[
		{"Sid":"AllowS3Read","Effect":"Allow","Action":["s3:Get*","s3:List*"],"Resource":"*"},
		{"Sid":"AllowEC2","Effect":"Allow","Action":"ec2:*","Resource":"*"},
		{"Sid":"DenyProdBucket","Effect":"Deny","Action":"s3:*","Resource":"arn:aws:s3:::prod-*"},
		{"Sid":"DenyTerminate","Effect":"Deny","Action":"ec2:TerminateInstances","Resource":"*"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// s3:GetObject on dev bucket: Allow (no Deny matches).
	assert.Equal(t, Allow,
		p.EvaluateAction("s3:GetObject", "arn:aws:s3:::dev-data"))

	// s3:GetObject on prod bucket: Allow matches, Deny matches → ExplicitDeny.
	assert.Equal(t, ExplicitDeny,
		p.EvaluateAction("s3:GetObject", "arn:aws:s3:::prod-data"))

	// s3:PutObject on dev bucket: neither statement matches the action → NoMatch.
	assert.Equal(t, NoMatch,
		p.EvaluateAction("s3:PutObject", "arn:aws:s3:::dev-data"))

	// ec2:RunInstances anywhere: Allow (wildcard ec2:*), no matching Deny.
	assert.Equal(t, Allow, p.EvaluateAction("ec2:RunInstances", "*"))

	// ec2:TerminateInstances: Allow (ec2:*) plus Deny → ExplicitDeny.
	assert.Equal(t, ExplicitDeny, p.EvaluateAction("ec2:TerminateInstances", "*"))

	// iam:CreateUser: no statement matches.
	assert.Equal(t, NoMatch, p.EvaluateAction("iam:CreateUser", "*"))
}

// TestEvaluateAction_Enum directly asserts each of the three distinct
// ActionDecision values is returned in the obvious minimal scenario.
func TestEvaluateAction_Enum(t *testing.T) {
	allowOnly, err := ParseIAMPolicy([]byte(
		`{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`))
	require.NoError(t, err)
	assert.Equal(t, Allow, allowOnly.EvaluateAction("iam:PassRole", "*"))
	assert.Equal(t, NoMatch, allowOnly.EvaluateAction("iam:CreateUser", "*"))

	allowThenDeny, err := ParseIAMPolicy([]byte(
		`{"Statement":[
			{"Effect":"Allow","Action":"*","Resource":"*"},
			{"Effect":"Deny","Action":"iam:DeleteUser","Resource":"*"}
		]}`))
	require.NoError(t, err)
	assert.Equal(t, ExplicitDeny, allowThenDeny.EvaluateAction("iam:DeleteUser", "*"))
	assert.Equal(t, Allow, allowThenDeny.EvaluateAction("iam:CreateUser", "*"))
}

// TestExplicitlyDenies pins the ExplicitlyDenies helper across the three
// cases (NoMatch, Allow, ExplicitDeny) so it can't drift from
// EvaluateAction's contract.
func TestExplicitlyDenies(t *testing.T) {
	doc := `{"Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"*"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	assert.True(t, p.ExplicitlyDenies("s3:DeleteBucket", "*"),
		"DeleteBucket should be explicitly denied")
	assert.False(t, p.ExplicitlyDenies("s3:GetObject", "*"),
		"GetObject is allowed, not denied")
	assert.False(t, p.ExplicitlyDenies("iam:CreateUser", "*"),
		"unmatched action is NoMatch, not ExplicitDeny")
}

// TestActionDecision_String verifies the enum String() method for log/debug.
func TestActionDecision_String(t *testing.T) {
	assert.Equal(t, "NoMatch", NoMatch.String())
	assert.Equal(t, "Allow", Allow.String())
	assert.Equal(t, "ExplicitDeny", ExplicitDeny.String())
	assert.Equal(t, "Unknown", ActionDecision(99).String())
}

// Tests for the condition-aware evaluator (EvaluateActionWithContext /
// AllowsActionWithContext) live in iam_parser_eval_ctx_test.go — split to
// keep this file under the 500-line hard cap.
