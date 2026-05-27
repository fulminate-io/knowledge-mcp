// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_policy_modification_test.go covers the five Phase 4 rules in
// iam_rules_policy_modification.go. One table-driven test exercises the
// positive path for every rule; standalone tests cover negative cases and
// rule-specific edge cases (self-promote, multi-role fan-out, AWS-managed
// policy filtering, lower-confidence 0.7 documentation for
// setDefaultPolicyVersion). Follows the fixture/helper style of
// iam_rules_credential_access_test.go and iam_rules_test.go.

// policyModRuleCase is one row of the table driving TestPolicyModificationRules.
type policyModRuleCase struct {
	name      string
	rule      iamRule
	setup     func(t *testing.T, fx *cloudFixture) (attackerID string)
	wantEdges int
	wantKinds []iamInferredEdgeKind
}

// TestPolicyModificationRules is the single table-driven test exercising the
// positive path for every Phase 4 rule. Each row builds a minimal fixture,
// invokes the rule, and asserts the total edge count plus the set of edge
// kinds produced. Rule-specific assertions (self-loop handling, fan-out
// shape, filtering) live in the standalone tests below.
func TestPolicyModificationRules(t *testing.T) {
	cases := []policyModRuleCase{
		{
			name: "put_user_policy_single_victim",
			rule: putUserPolicyRule,
			setup: func(t *testing.T, fx *cloudFixture) string {
				attacker := addIAMUserWithInline(t, fx, accountA,
					"arn:aws:iam::111111111111:user/attacker", "attacker",
					"pu", `{"Statement":[{"Effect":"Allow","Action":"iam:PutUserPolicy","Resource":"*"}]}`)
				addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")
				return attacker.Id
			},
			// 1 impersonate (attacker → victim) + 1 attach_policy self-loop.
			wantEdges: 2,
			wantKinds: []iamInferredEdgeKind{iamEdgeImpersonate, iamEdgeAttachPolicy},
		},
		{
			name: "put_group_policy_single_group_member",
			rule: putGroupPolicyRule,
			setup: func(t *testing.T, fx *cloudFixture) string {
				attacker := addIAMUserWithInline(t, fx, accountA,
					"arn:aws:iam::111111111111:user/attacker", "attacker",
					"pg", `{"Statement":[{"Effect":"Allow","Action":"iam:PutGroupPolicy","Resource":"*"}]}`)
				member := addIAMUser(t, fx, accountA,
					"arn:aws:iam::111111111111:user/member", "member")
				addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
				addGroupMembership(t, fx, accountA, member.Id, "arn:aws:iam::111111111111:group/devs")
				return attacker.Id
			},
			// 1 impersonate (attacker → member) + 1 attach_policy self-loop.
			wantEdges: 2,
			wantKinds: []iamInferredEdgeKind{iamEdgeImpersonate, iamEdgeAttachPolicy},
		},
		{
			name: "put_role_policy_single_role",
			rule: putRolePolicyRule,
			setup: func(t *testing.T, fx *cloudFixture) string {
				attacker := addIAMUserWithInline(t, fx, accountA,
					"arn:aws:iam::111111111111:user/attacker", "attacker",
					"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PutRolePolicy","Resource":"*"}]}`)
				addIAMRoleWithTrust(t, fx, accountA,
					"arn:aws:iam::111111111111:role/target", "target",
					`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
				return attacker.Id
			},
			// 1 execute_as (attacker → target role) + 1 attach_policy self-loop.
			wantEdges: 2,
			wantKinds: []iamInferredEdgeKind{iamEdgeExecuteAs, iamEdgeAttachPolicy},
		},
		{
			name: "create_policy_version_single_victim",
			rule: createPolicyVersionRule,
			setup: func(t *testing.T, fx *cloudFixture) string {
				attacker := addIAMUserWithInline(t, fx, accountA,
					"arn:aws:iam::111111111111:user/attacker", "attacker",
					"cpv", `{"Statement":[{"Effect":"Allow","Action":"iam:CreatePolicyVersion","Resource":"*"}]}`)
				victim := addIAMUser(t, fx, accountA,
					"arn:aws:iam::111111111111:user/victim", "victim")
				addManagedPolicy(t, fx, accountA,
					"arn:aws:iam::111111111111:policy/CustomerX", "CustomerX",
					`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
				attachPolicy(t, fx, accountA, victim.Id,
					"arn:aws:iam::111111111111:policy/CustomerX")
				return attacker.Id
			},
			// 1 impersonate (attacker → victim, who holds customer-managed).
			// Attacker itself does NOT hold a customer-managed policy → no
			// self-loop.
			wantEdges: 1,
			wantKinds: []iamInferredEdgeKind{iamEdgeImpersonate},
		},
		{
			name: "set_default_policy_version_single_victim",
			rule: setDefaultPolicyVersionRule,
			setup: func(t *testing.T, fx *cloudFixture) string {
				attacker := addIAMUserWithInline(t, fx, accountA,
					"arn:aws:iam::111111111111:user/attacker", "attacker",
					"sdpv", `{"Statement":[{"Effect":"Allow","Action":"iam:SetDefaultPolicyVersion","Resource":"*"}]}`)
				victim := addIAMUser(t, fx, accountA,
					"arn:aws:iam::111111111111:user/victim", "victim")
				addManagedPolicy(t, fx, accountA,
					"arn:aws:iam::111111111111:policy/CustomerX", "CustomerX",
					`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
				attachPolicy(t, fx, accountA, victim.Id,
					"arn:aws:iam::111111111111:policy/CustomerX")
				return attacker.Id
			},
			wantEdges: 1,
			wantKinds: []iamInferredEdgeKind{iamEdgeImpersonate},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCloudFixture(t)
			_ = tc.setup(t, fx)

			rctx := newTestRuleContext(t, fx, accountA)
			edges, err := tc.rule(newTestCtx(t), rctx)
			require.NoError(t, err)
			require.Len(t, edges, tc.wantEdges, "unexpected edge count")

			gotKinds := make(map[iamInferredEdgeKind]bool, len(edges))
			for _, e := range edges {
				gotKinds[e.Kind] = true
			}
			for _, wk := range tc.wantKinds {
				assert.Truef(t, gotKinds[wk], "expected edge kind %q not emitted", wk)
			}
		})
	}
}

// TestPolicyModificationRules_Negative exercises the no-action path for every
// rule. None should fire if the caller lacks the action.
func TestPolicyModificationRules_Negative(t *testing.T) {
	rules := map[string]iamRule{
		"put_user_policy":            putUserPolicyRule,
		"put_group_policy":           putGroupPolicyRule,
		"put_role_policy":            putRolePolicyRule,
		"create_policy_version":      createPolicyVersionRule,
		"set_default_policy_version": setDefaultPolicyVersionRule,
	}
	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			fx := newCloudFixture(t)
			addIAMUserWithInline(t, fx, accountA,
				"arn:aws:iam::111111111111:user/alice", "alice",
				"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
			addIAMUser(t, fx, accountA,
				"arn:aws:iam::111111111111:user/bob", "bob")
			addIAMRoleWithTrust(t, fx, accountA,
				"arn:aws:iam::111111111111:role/r1", "r1",
				`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
			addIAMGroup(t, fx,
				"arn:aws:iam::111111111111:group/g1", "g1")

			rctx := newTestRuleContext(t, fx, accountA)
			edges, err := rule(newTestCtx(t), rctx)
			require.NoError(t, err)
			assert.Empty(t, edges, "rule must not fire without its action")
		})
	}
}

// TestPutUserPolicyRule_SelfPromote verifies that a caller which is itself
// an iam-user emits the attachPolicy self-loop edge in addition to any
// impersonate edges. With only the attacker present, no other user exists,
// so the only edge is the self-loop.
func TestPutUserPolicyRule_SelfPromote(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/solo", "solo",
		"pu", `{"Statement":[{"Effect":"Allow","Action":"iam:PutUserPolicy","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := putUserPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1, "expected only the self-promote edge")
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, attacker.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeAttachPolicy, edges[0].Kind)
}

// TestPutRolePolicyRule_MultipleRoles verifies N target roles produce N
// execute_as edges (plus 1 attach_policy self-loop on the caller). Three
// roles → four total edges.
func TestPutRolePolicyRule_MultipleRoles(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PutRolePolicy","Resource":"*"}]}`)
	trust := `{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	r1 := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/r1", "r1", trust)
	r2 := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/r2", "r2", trust)
	r3 := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/r3", "r3", trust)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := putRolePolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 4, "expected 3 execute_as edges + 1 self-loop")

	targets := map[string]bool{r1.Id: false, r2.Id: false, r3.Id: false}
	var selfLoops int
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		switch e.Kind {
		case iamEdgeExecuteAs:
			_, ok := targets[e.ToID]
			assert.True(t, ok, "execute_as target %q not in role set", e.ToID)
			targets[e.ToID] = true
		case iamEdgeAttachPolicy:
			assert.Equal(t, attacker.Id, e.ToID, "attachPolicy must be a self-loop")
			selfLoops++
		default:
			t.Fatalf("unexpected edge kind %q", e.Kind)
		}
	}
	assert.Equal(t, 1, selfLoops)
	for id, hit := range targets {
		assert.Truef(t, hit, "role %q never received an execute_as edge", id)
	}
}

// TestCreatePolicyVersionRule_IgnoresAWSManagedPolicies verifies the rule
// only targets principals attached to CUSTOMER-managed policies. A victim
// attached solely to AdministratorAccess (an AWS-managed policy under
// arn:aws:iam::aws:policy/) must not appear in the target set.
func TestCreatePolicyVersionRule_IgnoresAWSManagedPolicies(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"cpv", `{"Statement":[{"Effect":"Allow","Action":"iam:CreatePolicyVersion","Resource":"*"}]}`)
	victim := addIAMUser(t, fx, accountA,
		"arn:aws:iam::111111111111:user/victim", "victim")
	// Attach ONLY AdministratorAccess (AWS-managed). No customer-managed
	// policy should land on the victim.
	addAdminAttachment(t, fx, accountA, victim.Id)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createPolicyVersionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges,
		"createPolicyVersion must not target principals whose only attached policy is AWS-managed")
}

// TestCreatePolicyVersionRule_CustomerManagedSelfPromote verifies the
// attacker itself holding a customer-managed policy emits an attachPolicy
// self-loop promoting to admin (in addition to the impersonate edges toward
// any other customer-managed-holding principal).
func TestCreatePolicyVersionRule_CustomerManagedSelfPromote(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"cpv", `{"Statement":[{"Effect":"Allow","Action":"iam:CreatePolicyVersion","Resource":"*"}]}`)
	addManagedPolicy(t, fx, accountA,
		"arn:aws:iam::111111111111:policy/CustomerX", "CustomerX",
		`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	attachPolicy(t, fx, accountA, attacker.Id,
		"arn:aws:iam::111111111111:policy/CustomerX")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createPolicyVersionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	// Attacker is the only customer-managed-holding principal → no
	// impersonate edges; only the self-loop.
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, attacker.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeAttachPolicy, edges[0].Kind)
}

// TestSetDefaultPolicyVersionRule_LowerConfidence asserts the 0.7 confidence
// is documented in the setDefaultPolicyVersionRule's reason strings (the
// only runtime-visible surface for confidence in v1.1 — registerIAMRule has
// no confidence parameter). The rule is still expected to fire; the
// assertion is about the documented confidence value.
func TestSetDefaultPolicyVersionRule_LowerConfidence(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"sdpv", `{"Statement":[{"Effect":"Allow","Action":"iam:SetDefaultPolicyVersion","Resource":"*"}]}`)
	victim := addIAMUser(t, fx, accountA,
		"arn:aws:iam::111111111111:user/victim", "victim")
	addManagedPolicy(t, fx, accountA,
		"arn:aws:iam::111111111111:policy/CustomerX", "CustomerX",
		`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	attachPolicy(t, fx, accountA, victim.Id,
		"arn:aws:iam::111111111111:policy/CustomerX")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := setDefaultPolicyVersionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.NotEmpty(t, edges, "rule must still fire with 0.7 confidence")
	for _, e := range edges {
		assert.Containsf(t, e.Reason, "0.7",
			"setDefaultPolicyVersion reason must document confidence 0.7: got %q", e.Reason)
	}
}

// TestPutGroupPolicyRule_FanOut verifies a group with multiple members
// produces one impersonate edge per member (plus 1 self-loop on the caller).
func TestPutGroupPolicyRule_FanOut(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA,
		"arn:aws:iam::111111111111:user/attacker", "attacker",
		"pg", `{"Statement":[{"Effect":"Allow","Action":"iam:PutGroupPolicy","Resource":"*"}]}`)
	m1 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m1", "m1")
	m2 := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/m2", "m2")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/devs", "devs")
	addGroupMembership(t, fx, accountA, m1.Id, "arn:aws:iam::111111111111:group/devs")
	addGroupMembership(t, fx, accountA, m2.Id, "arn:aws:iam::111111111111:group/devs")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := putGroupPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	// 2 impersonate + 1 attach_policy self-loop.
	require.Len(t, edges, 3)

	impersonateTargets := map[string]bool{m1.Id: false, m2.Id: false}
	var selfLoops int
	for _, e := range edges {
		assert.Equal(t, attacker.Id, e.FromID)
		switch e.Kind {
		case iamEdgeImpersonate:
			_, ok := impersonateTargets[e.ToID]
			assert.True(t, ok, "impersonate target %q not in member set", e.ToID)
			impersonateTargets[e.ToID] = true
		case iamEdgeAttachPolicy:
			assert.Equal(t, attacker.Id, e.ToID)
			selfLoops++
		default:
			t.Fatalf("unexpected edge kind %q", e.Kind)
		}
	}
	assert.Equal(t, 1, selfLoops)
	for id, hit := range impersonateTargets {
		assert.Truef(t, hit, "member %q never received an impersonate edge", id)
	}
}
