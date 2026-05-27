// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// iam_rules_test.go covers the seven IAM inference rules. Each rule has at
// least one positive case (rule fires when expected) and one negative case
// (rule does NOT fire when expected). Tests share the cloudFixture and
// helpers in iam_fixture_test.go.

const accountA = "111111111111"
const accountB = "222222222222"

// TestIAMRulesRegistered pins the registered rule set so an accidental
// rename or deletion fails the test rather than silently shipping a
// missing rule. Covers v1.1 (seven rules), the v2 credential-access
// additions (create_login_profile, update_login_profile), the v2
// policy-modification additions (put_user_policy, put_group_policy,
// put_role_policy, create_policy_version, set_default_policy_version),
// the v2 compute PassRole additions (glue_create_dev_endpoint,
// sagemaker_create_notebook, sagemaker_create_training,
// codebuild_create_project, codebuild_update_project,
// cloudformation_create_stack, datapipeline_create_pipeline, ecs_run_task),
// and the v2 Lambda update additions (update_function_code,
// update_function_configuration).
func TestIAMRulesRegistered(t *testing.T) {
	want := []string{
		"assume_role_trust_policy",
		"wildcard_action",
		"attach_policy",
		"pass_role_lambda",
		"run_instances",
		"create_function",
		"create_access_key",
		"create_login_profile",
		"update_login_profile",
		"put_user_policy",
		"put_group_policy",
		"put_role_policy",
		"create_policy_version",
		"set_default_policy_version",
		"update_assume_role_policy",
		"add_user_to_group",
		"glue_create_dev_endpoint",
		"sagemaker_create_notebook",
		"sagemaker_create_training",
		"codebuild_create_project",
		"codebuild_update_project",
		"cloudformation_create_stack",
		"datapipeline_create_pipeline",
		"ecs_run_task",
		"update_function_code",
		"update_function_configuration",
	}
	for _, name := range want {
		_, ok := lookupIAMRule(name)
		assert.True(t, ok, "expected rule %q to be registered", name)
	}

	got := allIAMRules()
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	assert.Equal(t, sortedWant, got, "rule set drift")
}

// TestAssumeRoleTrustPolicyRule_AllowsUserPositive verifies that a role
// whose trust policy allows a specific user ARN emits assume_role from the
// user to the role.
func TestAssumeRoleTrustPolicyRule_AllowsUserPositive(t *testing.T) {
	fx := newCloudFixture(t)

	user := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice")
	role := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/alice"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := assumeRoleTrustPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, role.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeAssumeRole, edges[0].Kind)
}

// TestAssumeRoleTrustPolicyRule_NoTrustNegative verifies that a role with no
// trust policy emits no edges.
func TestAssumeRoleTrustPolicyRule_NoTrustNegative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/orphan", "orphan", "")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := assumeRoleTrustPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestAssumeRoleTrustPolicyRule_CrossAccountPrincipal verifies that a trust
// policy referencing a principal in a different account still emits an
// edge (with the cross-account ARN as FromID).
func TestAssumeRoleTrustPolicyRule_CrossAccountPrincipal(t *testing.T) {
	fx := newCloudFixture(t)
	// Account B has a user that account A's role trusts.
	addIAMUser(t, fx, accountB, "arn:aws:iam::222222222222:user/bob", "bob")
	role := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/admin", "admin",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:user/bob"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := assumeRoleTrustPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "arn:aws:iam::222222222222:user/bob", edges[0].FromID)
	assert.Equal(t, role.Id, edges[0].ToID)
}

// TestWildcardActionRule_Positive verifies a user with an admin inline
// policy emits an effective_admin self-loop.
func TestWildcardActionRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	user := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"adm", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := wildcardActionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, user.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeEffectiveAdmin, edges[0].Kind)
}

// TestWildcardActionRule_ManagedPolicyAdmin verifies admin via managed
// policy attachment.
func TestWildcardActionRule_ManagedPolicyAdmin(t *testing.T) {
	fx := newCloudFixture(t)
	user := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice")
	addAdminAttachment(t, fx, accountA, user.Id)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := wildcardActionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
}

// TestWildcardActionRule_NoAdminNegative verifies a user with a benign
// policy emits no admin self-loop.
func TestWildcardActionRule_NoAdminNegative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"ro", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := wildcardActionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestAttachPolicyRule_Positive verifies attach_policy fires for a
// principal that can call iam:AttachUserPolicy.
func TestAttachPolicyRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	user := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"att", `{"Statement":[{"Effect":"Allow","Action":"iam:AttachUserPolicy","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := attachPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, iamEdgeAttachPolicy, edges[0].Kind)
}

// TestAttachPolicyRule_Negative verifies attach_policy does not fire when
// the principal cannot attach policies.
func TestAttachPolicyRule_Negative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := attachPolicyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestPassRoleLambdaRule_Positive verifies pass_role_lambda fires when a
// user can pass a role to Lambda and the target role trusts Lambda.
func TestPassRoleLambdaRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	user := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)
	role := addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/lambda-exec", "lambda-exec",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := passRoleLambdaRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, user.Id, edges[0].FromID)
	assert.Equal(t, role.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeExecuteAs, edges[0].Kind)
}

// TestPassRoleLambdaRule_NoLambdaTrustNegative verifies the rule does not
// fire if no role trusts Lambda.
func TestPassRoleLambdaRule_NoLambdaTrustNegative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/ec2-exec", "ec2-exec",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := passRoleLambdaRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestRunInstancesRule_Positive verifies run_instances fires for an EC2
// trust target.
func TestRunInstancesRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"pr", `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/ec2-exec", "ec2-exec",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := runInstancesRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
}

// TestCreateFunctionRule_Positive verifies create_function fires for a
// lambda:CreateFunction caller and a Lambda trust target.
func TestCreateFunctionRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice",
		"cf", `{"Statement":[{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/lambda-exec", "lambda-exec",
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createFunctionRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
}

// TestCreateAccessKeyRule_Positive verifies a principal that can mint keys
// emits impersonate edges to every other user.
func TestCreateAccessKeyRule_Positive(t *testing.T) {
	fx := newCloudFixture(t)
	attacker := addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/attacker", "attacker",
		"ak", `{"Statement":[{"Effect":"Allow","Action":"iam:CreateAccessKey","Resource":"*"}]}`)
	victim := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/victim", "victim")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createAccessKeyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, attacker.Id, edges[0].FromID)
	assert.Equal(t, victim.Id, edges[0].ToID)
	assert.Equal(t, iamEdgeImpersonate, edges[0].Kind)
}

// TestCreateAccessKeyRule_Negative verifies the rule does not fire for a
// principal lacking iam:CreateAccessKey.
func TestCreateAccessKeyRule_Negative(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice")
	addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/bob", "bob")

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := createAccessKeyRule(newTestCtx(t), rctx)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestIterPrincipalPolicies_GroupInheritance verifies a user inherits its
// group's policies. This is the EdgeMemberOf integration point.
func TestIterPrincipalPolicies_GroupInheritance(t *testing.T) {
	fx := newCloudFixture(t)
	user := addIAMUser(t, fx, accountA, "arn:aws:iam::111111111111:user/alice", "alice")
	addIAMGroup(t, fx, "arn:aws:iam::111111111111:group/admins", "admins")
	addInlinePolicy(t, fx, "arn:aws:iam::111111111111:group/admins", "adm",
		`{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	fx.AddEdge(accountA, user.Id, "arn:aws:iam::111111111111:group/admins", kgtypes.EdgeMemberOf)

	rctx := newTestRuleContext(t, fx, accountA)
	// Re-fetch user from scoped DB so we get the latest version (the upsert
	// above replaced the underlying group, but the user is unchanged — still
	// fine to use rctx.Users[0]).
	policies := iterPrincipalPolicies(newTestCtx(t), rctx, rctx.Users[0])
	require.NotEmpty(t, policies, "user should inherit group policies")
	// At least one of the inherited policies must be effective admin.
	var sawAdmin bool
	for _, p := range policies {
		if p.IsEffectiveAdmin() {
			sawAdmin = true
		}
	}
	assert.True(t, sawAdmin, "user should inherit admin via group membership")
}
