// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// iam_escalation_test.go covers the end-to-end IAMEscalationAnalyzer.
// Each test builds a synthetic cloud fixture, calls Run, and asserts on
// the resulting findings (count, severity, evidence shape, metrics).

// TestIAMEscalationAnalyzer_Registered verifies the analyzer self-registers
// at init() time so the topology dispatch layer can find it by name.
func TestIAMEscalationAnalyzer_Registered(t *testing.T) {
	a, ok := Get("iam_escalation")
	require.True(t, ok, "iam_escalation analyzer must be registered")
	assert.Equal(t, "iam_escalation", a.Name())
}

// TestIAMEscalationAnalyzer_NonCloudGraph verifies non-cloud graphs return
// (nil, nil) without error — the analyzer should be safe to dispatch
// against any graph type.
func TestIAMEscalationAnalyzer_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	a := IAMEscalationAnalyzer{}
	findings, err := a.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphKnowledge,
		Name:   "default",
	})
	require.NoError(t, err)
	assert.Nil(t, findings)
}

// TestIAMEscalationAnalyzer_NoEscalation verifies a clean account with no
// escalation path produces no findings. The fixture has one admin role
// (with AdministratorAccess attached) and one developer role with no
// escalation policies.
func TestIAMEscalationAnalyzer_NoEscalation(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/dev", "dev",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	findings := runAnalyzer(t, fx, accountA, 0)
	assert.Empty(t, findings, "no escalation paths in clean fixture")
}

// TestIAMEscalationAnalyzer_DirectAssumeRole verifies a single-hop
// escalation: dev user can assume admin role via the role's trust policy.
func TestIAMEscalationAnalyzer_DirectAssumeRole(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/dev", "dev")
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/dev"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	findings := runAnalyzer(t, fx, accountA, 0)
	require.Len(t, findings, 1)
	f := findings[0]
	assert.Equal(t, SeverityCritical, f.Severity)
	assert.InDelta(t, 1, f.Metrics["hop_count"], 1e-9)
	assert.Equal(t, "arn:aws:iam::111111111111:user/dev", f.Evidence[0])
}

// TestIAMEscalationAnalyzer_WildcardEffectiveAdmin verifies effective admin
// via inline wildcard policy is detected — but produces NO escalation
// finding because the principal is already admin (no escalation needed).
func TestIAMEscalationAnalyzer_WildcardEffectiveAdmin(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"adm", `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)

	findings := runAnalyzer(t, fx, accountA, 0)
	// Alice IS admin so there's no path to find. The point of this test is
	// to ensure dispatchIAMRules adds her to the admin set rather than
	// flagging her as an escalation source.
	assert.Empty(t, findings, "admin principals are not escalation sources")
}

// TestIAMEscalationAnalyzer_PassRoleLambda verifies the multi-rule
// composition path: user can pass-role to a lambda-trust admin role.
func TestIAMEscalationAnalyzer_PassRoleLambda(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/dev", "dev",
		"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/lambda-admin", "lambda-admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/lambda-admin")

	findings := runAnalyzer(t, fx, accountA, 0)
	require.NotEmpty(t, findings, "expected escalation via PassRole+Lambda")
	// Find the dev→lambda-admin path.
	var match *Finding
	for i := range findings {
		if findings[i].Evidence[0] == "arn:aws:iam::111111111111:user/dev" {
			match = &findings[i]
			break
		}
	}
	require.NotNil(t, match, "no finding from dev")
	assert.Equal(t, SeverityCritical, match.Severity)
	assert.InDelta(t, 1, match.Metrics["hop_count"], 1e-9)
}

// TestIAMEscalationAnalyzer_MultiHop verifies a 2-hop escalation: dev → mid
// (via assume-role) → admin (via assume-role).
func TestIAMEscalationAnalyzer_MultiHop(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/dev", "dev")
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/mid", "mid",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/dev"},"Action":"sts:AssumeRole"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:role/mid"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	findings := runAnalyzer(t, fx, accountA, 0)
	require.NotEmpty(t, findings)
	// Find the dev path; it should be 2 hops.
	var match *Finding
	for i := range findings {
		if findings[i].Evidence[0] == "arn:aws:iam::111111111111:user/dev" {
			match = &findings[i]
			break
		}
	}
	require.NotNil(t, match)
	assert.InDelta(t, 2, match.Metrics["hop_count"], 1e-9)
}

// TestIAMEscalationAnalyzer_CrossAccount verifies that a trust policy in
// account A allowing a user in account B produces a finding with
// has_cross_account=1. This is the locked OQ-2 behavior.
func TestIAMEscalationAnalyzer_CrossAccount(t *testing.T) {
	fx := newCloudFixture(t)
	// Account B: a regular user.
	addIAMUser(t, fx, accountB, "arn:aws:iam::222222222222:user/bob", "bob")
	// Account A: an admin role that trusts bob from account B.
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:user/bob"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	findings := runAnalyzer(t, fx, accountA, 0)
	require.NotEmpty(t, findings, "expected cross-account escalation finding")
	// Find the bob path.
	var match *Finding
	for i := range findings {
		if findings[i].Evidence[0] == "arn:aws:iam::222222222222:user/bob" {
			match = &findings[i]
			break
		}
	}
	require.NotNil(t, match, "expected finding sourced from cross-account user bob")
	assert.InDelta(t, 1, match.Metrics["has_cross_account"], 1e-9, "cross-account flag should be set")
}

// TestIAMEscalationAnalyzer_TopK verifies the TopK cap.
func TestIAMEscalationAnalyzer_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	// Three different users, all able to assume the admin role.
	for _, name := range []string{"alice", "bob", "carol"} {
		arn := "arn:aws:iam::111111111111:user/" + name
		addIAMUser(t, fx, accountA, arn, name)
	}
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111111111111:user/alice","arn:aws:iam::111111111111:user/bob","arn:aws:iam::111111111111:user/carol"]},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	findings := runAnalyzer(t, fx, accountA, 2)
	assert.Len(t, findings, 2, "TopK=2 should cap findings to 2")
}

// TestIAMEscalationAnalyzer_ContextCancel verifies an already-cancelled
// context produces a clean error (no findings, non-nil err).
func TestIAMEscalationAnalyzer_ContextCancel(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice")
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/alice"},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, accountA, "arn:aws:iam::111111111111:role/admin")

	ctx, cancel := context.WithCancel(newTestCtx(t))
	cancel()

	a := IAMEscalationAnalyzer{}
	_, err := a.Run(ctx, Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   accountA,
	})
	require.Error(t, err)
}

// TestHumanKindLabel covers every registered edge kind plus the unknown
// fallback. Each label must be non-empty and grammatically fit into a
// "<source> <verb phrase> <target>" sentence (Phase 9 Step 4 / Step 2).
func TestHumanKindLabel(t *testing.T) {
	cases := []struct {
		kind iamInferredEdgeKind
		want string
	}{
		{iamEdgeAssumeRole, "can assume role"},
		{iamEdgeExecuteAs, "can execute code as"},
		{iamEdgeImpersonate, "can impersonate"},
		{iamEdgeAttachPolicy, "can attach an admin-equivalent policy to"},
		{iamEdgeEffectiveAdmin, "is effective admin of"},
		{iamInferredEdgeKind("unknown_future_kind"), "can escalate to"},
	}
	for _, tc := range cases {
		got := humanKindLabel(tc.kind)
		assert.Equal(t, tc.want, got, "humanKindLabel(%q)", tc.kind)
		assert.NotEmpty(t, got, "label must be non-empty for kind %q", tc.kind)
	}
}

// runAnalyzer is a tiny helper that constructs the Request and runs the
// analyzer for one account. Centralized to keep the test functions short.
//
//nolint:unparam // account kept on signature for future multi-account tests
func runAnalyzer(t *testing.T, fx *cloudFixture, account string, topK int) []Finding {
	t.Helper()
	a := IAMEscalationAnalyzer{}
	findings, err := a.Run(newTestCtx(t), fx.cloudReq(account, topK))
	require.NoError(t, err)
	return findings
}
