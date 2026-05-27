// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_parser_trust_test.go covers the NotPrincipal-aware trust-policy
// methods (TrustPrincipalMatches + IsTrustPolicyWideOpen). Each test pins
// one AWS-semantic case of NotPrincipal inversion so a failure points at
// the exact rule that broke.

// TestTrustPolicy_NotPrincipalWildcard_WideOpen verifies the most extreme
// wide-open case: Effect=Allow + NotPrincipal listing a single ARN means
// any ARN in the world EXCEPT that one can assume the role.
func TestTrustPolicy_NotPrincipalWildcard_WideOpen(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","NotPrincipal":{"AWS":"arn:aws:iam::111111111111:user/blocked"},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Any arbitrary principal matches because it's NOT in the excluded list.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:user/alice"))
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::333333333333:role/attacker"))
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::999999999999:root"))

	// The one excluded principal must NOT match.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/blocked"))

	// IsTrustPolicyWideOpen reports the wide-open flag and the exception list.
	wide, except := p.IsTrustPolicyWideOpen()
	assert.True(t, wide)
	assert.Equal(t, []string{"arn:aws:iam::111111111111:user/blocked"}, except)
}

// TestTrustPolicy_NotPrincipalList_WideOpenExceptList verifies a larger
// exception list and that IsTrustPolicyWideOpen names each excluded principal.
func TestTrustPolicy_NotPrincipalList_WideOpenExceptList(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","NotPrincipal":{"AWS":[
			"arn:aws:iam::111111111111:user/alice",
			"arn:aws:iam::111111111111:user/bob"
		]},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Anyone not in the exception list can assume.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/charlie"))
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:role/external"))

	// Alice and Bob are excluded.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/bob"))

	wide, except := p.IsTrustPolicyWideOpen()
	assert.True(t, wide)
	assert.ElementsMatch(t, []string{
		"arn:aws:iam::111111111111:user/alice",
		"arn:aws:iam::111111111111:user/bob",
	}, except)
}

// TestTrustPolicy_NotPrincipal_DenyInverted verifies Effect=Deny+NotPrincipal:
// "deny everyone EXCEPT alice". Alice must still be allowed (no Deny match,
// and a separate Allow Principal covers her), everyone else is denied.
func TestTrustPolicy_NotPrincipal_DenyInverted(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"},
		{"Effect":"Deny","NotPrincipal":{"AWS":"arn:aws:iam::111111111111:user/alice"},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Alice is NOT matched by the Deny+NotPrincipal (she's in the exception
	// list), so only the Allow applies → she can assume.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))

	// Everyone else IS matched by the Deny+NotPrincipal → denied.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/bob"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:role/external"))

	// This is NOT a wide-open-Allow case (the wide-openness comes from the
	// Allow Principal=="*", not from a NotPrincipal). The rule layer handles
	// Allow+Principal=="*" elsewhere; IsTrustPolicyWideOpen reports false here
	// because no Allow statement uses NotPrincipal.
	wide, except := p.IsTrustPolicyWideOpen()
	assert.False(t, wide)
	assert.Nil(t, except)
}

// TestTrustPolicy_PrincipalAndNotPrincipalMixed verifies graceful handling
// of the AWS-illegal case where a single statement sets both Principal and
// NotPrincipal. We treat such statements as no-match rather than guessing.
func TestTrustPolicy_PrincipalAndNotPrincipalMixed(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow",
		 "Principal":{"AWS":"arn:aws:iam::111111111111:user/alice"},
		 "NotPrincipal":{"AWS":"arn:aws:iam::111111111111:user/bob"},
		 "Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// The malformed statement matches no-one — neither alice (named in
	// Principal) nor the rest of the world (implied by NotPrincipal).
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/bob"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:role/external"))

	// And it's not reported as wide-open either.
	wide, except := p.IsTrustPolicyWideOpen()
	assert.False(t, wide)
	assert.Nil(t, except)
}

// TestIsTrustPolicyWideOpen_NoNotPrincipal verifies a normal trust policy
// with only regular Principal entries is NOT reported as wide-open.
func TestIsTrustPolicyWideOpen_NoNotPrincipal(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/alice"},"Action":"sts:AssumeRole"},
		{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	wide, except := p.IsTrustPolicyWideOpen()
	assert.False(t, wide)
	assert.Nil(t, except)

	// Alice still matches normally; bob does not.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/bob"))
}

// TestIsTrustPolicyWideOpen_MultipleStatements verifies the exception list
// is the union of all excluded principals across multiple Allow+NotPrincipal
// statements, deduplicated.
func TestIsTrustPolicyWideOpen_MultipleStatements(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","NotPrincipal":{"AWS":[
			"arn:aws:iam::111111111111:user/alice",
			"arn:aws:iam::111111111111:user/bob"
		]},"Action":"sts:AssumeRole"},
		{"Effect":"Allow","NotPrincipal":{"AWS":[
			"arn:aws:iam::111111111111:user/bob",
			"arn:aws:iam::111111111111:user/charlie"
		]},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	wide, except := p.IsTrustPolicyWideOpen()
	assert.True(t, wide)
	// Union: alice, bob, charlie — each appearing exactly once.
	assert.ElementsMatch(t, []string{
		"arn:aws:iam::111111111111:user/alice",
		"arn:aws:iam::111111111111:user/bob",
		"arn:aws:iam::111111111111:user/charlie",
	}, except)

	// TrustPrincipalMatches still evaluates per-statement — a principal is
	// matched if it's NOT in *some* Allow-NotPrincipal list. alice is in
	// the first statement but NOT in the second → second statement allows her.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))
	// bob is in BOTH exception lists → no Allow matches him.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/bob"))
	// charlie is in the second but not the first → first allows him.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/charlie"))
	// Anyone else (not listed anywhere) is allowed by both statements.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:role/external"))
}

// TestTrustPolicy_NotPrincipalStar_ExcludesEveryone verifies the degenerate
// case of NotPrincipal=="*" is treated as "excludes everyone" rather than
// wide-open. This case is technically AWS-invalid but we handle it safely:
// the statement grants access to no-one and is not reported as wide-open.
func TestTrustPolicy_NotPrincipalStar_ExcludesEveryone(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","NotPrincipal":"*","Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// Nobody matches — NotPrincipal=="*" excludes everyone.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111111111111:user/alice"))
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::222222222222:role/external"))

	// Not reported as wide-open.
	wide, except := p.IsTrustPolicyWideOpen()
	assert.False(t, wide)
	assert.Nil(t, except)
}

// TestTrustPolicy_NilAndEmpty verifies nil/empty inputs don't panic.
func TestTrustPolicy_NilAndEmpty(t *testing.T) {
	var nilPolicy *IAMPolicy
	assert.False(t, nilPolicy.TrustPrincipalMatches("arn:aws:iam::111:user/alice"))
	wide, except := nilPolicy.IsTrustPolicyWideOpen()
	assert.False(t, wide)
	assert.Nil(t, except)

	// Empty ARN never matches.
	doc := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"sts:AssumeRole"}]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)
	assert.False(t, p.TrustPrincipalMatches(""))

	// But "*" Principal does match any real ARN.
	assert.True(t, p.TrustPrincipalMatches("arn:aws:iam::111:user/alice"))
}

// TestTrustPolicy_ServicePrincipalMatchesViaPrincipalContains verifies a
// regular Principal.Service statement does not leak through TrustPrincipalMatches
// for an AWS ARN — service principals are a separate dimension and shouldn't
// match ARN lookups. This pins the intentional split.
func TestTrustPolicy_ServicePrincipalDoesNotMatchARN(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}
	]}`
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err)

	// ARN-based lookup returns false — service principals are handled
	// by ServicePrincipals() not TrustPrincipalMatches.
	assert.False(t, p.TrustPrincipalMatches("arn:aws:iam::111:user/alice"))

	// Not wide-open.
	wide, _ := p.IsTrustPolicyWideOpen()
	assert.False(t, wide)
}
