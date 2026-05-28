// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// iam_fixture_test.go provides helper functions for building synthetic IAM
// graphs in the exposure cloudFixture. The helpers wrap the raw
// AddCloudResource / AddEdge primitives with IAM-specific shapes:
//
//   - addIAMRoleWithTrust(account, arn, name, trustPolicyJSON)
//   - addIAMUser(account, arn, name)
//   - addIAMUserWithInline(account, arn, name, inlineDoc)
//   - addIAMGroup(account, arn, name)
//   - addAdminAttachment(account, principalARN) — attaches AdministratorAccess
//   - addInlinePolicy(account, principalARN, inlineName, doc)
//   - addManagedPolicy(account, arn, name, document)
//   - attachPolicy(account, principalARN, policyARN)
//
// All helpers shape data the same way the AWS collector emits it: trust
// policies live in role node Content under "AssumeRolePolicyDocument" (URL
// encoded), inline policies live as metadata "inline_policy_<name>" (URL
// encoded), managed policy bodies live in iam-policy Content as a
// {"document": "<URL-encoded JSON>"} envelope.

const adminPolicyARN = "arn:aws:iam::aws:policy/AdministratorAccess"

// addIAMRoleWithTrust creates an iam-role node with a JSON-marshaled
// AssumeRolePolicyDocument. The trust policy JSON is URL-encoded to match
// the SDK shape.
func addIAMRoleWithTrust(t *testing.T, fx *cloudFixture, account, arn, name, trustPolicyJSON string) *knowledgev1.Node {
	t.Helper()
	encoded := url.QueryEscape(trustPolicyJSON)
	roleContent, err := json.Marshal(map[string]any{
		"Arn":                      arn,
		"RoleName":                 name,
		"AssumeRolePolicyDocument": encoded,
	})
	require.NoError(t, err)
	return fx.AddCloudResourceWithContent(account, arn, name, "iam-role", string(roleContent), nil)
}

// addIAMUser creates an iam-user node with no inline policies.
func addIAMUser(t *testing.T, fx *cloudFixture, account, arn, name string) *knowledgev1.Node {
	t.Helper()
	return fx.AddCloudResource(account, arn, name, "iam-user", nil)
}

// addIAMUserWithInline creates an iam-user node with one inline policy.
// The inline doc is URL-encoded to match the collector contract.
//
//nolint:unparam // account kept on signature for future cross-account tests
func addIAMUserWithInline(t *testing.T, fx *cloudFixture, account, arn, name, inlineName, doc string) *knowledgev1.Node {
	t.Helper()
	meta := map[string]string{
		"inline_policy_" + inlineName: url.QueryEscape(doc),
	}
	return fx.AddCloudResource(account, arn, name, "iam-user", meta)
}

// addIAMGroup creates an iam-group node with no inline policies. The account
// is hardcoded to accountA because every call site uses it.
func addIAMGroup(t *testing.T, fx *cloudFixture, arn, name string) {
	t.Helper()
	fx.AddCloudResource(accountA, arn, name, "iam-group", nil)
}

// addGroupMembership wires both directions of the group-membership relation:
// EdgeMemberOf (user → group) for iterPrincipalPolicies's forward walk and
// EdgeHasMember (group → user) for the per-group rules.
//
//nolint:unparam // account kept on signature for future cross-account tests
func addGroupMembership(t *testing.T, fx *cloudFixture, account, userARN, groupARN string) {
	t.Helper()
	fx.AddEdge(account, userARN, groupARN, kgtypes.EdgeMemberOf)
	fx.AddEdge(account, groupARN, userARN, kgtypes.EdgeHasMember)
}

// addInlinePolicy attaches an inline policy to an existing principal node by
// updating its metadata. The doc is URL-encoded. Account hardcoded to accountA.
func addInlinePolicy(t *testing.T, fx *cloudFixture, principalARN, inlineName, doc string) {
	t.Helper()
	fx.setNodeMeta(accountA, principalARN, "inline_policy_"+inlineName, url.QueryEscape(doc))
}

// addManagedPolicy creates an iam-policy node with the given document body
// wrapped in the managedPolicyContent envelope shape.
func addManagedPolicy(t *testing.T, fx *cloudFixture, account, arn, name, document string) {
	t.Helper()
	envelope, err := json.Marshal(map[string]any{
		"document": url.QueryEscape(document),
	})
	require.NoError(t, err)
	fx.AddCloudResourceWithContent(account, arn, name, "iam-policy", string(envelope), nil)
}

// attachPolicy creates an EdgeGrants edge from principal → policy.
func attachPolicy(t *testing.T, fx *cloudFixture, account, principalARN, policyARN string) {
	t.Helper()
	fx.AddEdge(account, principalARN, policyARN, kgtypes.EdgeGrants)
}

// addAdminAttachment creates an AdministratorAccess managed policy node and
// attaches it to the given principal. The document body is the canonical
// effective-admin shape (Allow * *).
func addAdminAttachment(t *testing.T, fx *cloudFixture, account, principalARN string) {
	t.Helper()
	// Create the AdministratorAccess policy node only once per account.
	if !fx.hasNode(account, adminPolicyARN) {
		addManagedPolicy(t, fx, account, adminPolicyARN, "AdministratorAccess",
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	}
	attachPolicy(t, fx, account, principalARN, adminPolicyARN)
}

// hasNode reports whether the named account already holds a node with the id.
func (f *cloudFixture) hasNode(account, id string) bool {
	acct := f.account(account)
	for i := range acct.nodes {
		if acct.nodes[i].id == id {
			return true
		}
	}
	return false
}

// loadPrincipals fetches every iam-role, iam-user, iam-group, and
// lambda-function node from a scoped account. Used to build the
// iamRuleContext for tests.
func loadPrincipals(t *testing.T, fx *cloudFixture, account string) (roles, users, groups, functions []*knowledgev1.Node) {
	t.Helper()
	acct := fx.account(account)
	for i := range acct.nodes {
		n := buildNode(&acct.nodes[i])
		switch nodeMeta(n, "resource_type") {
		case "iam-role":
			roles = append(roles, n)
		case "iam-user":
			users = append(users, n)
		case "iam-group":
			groups = append(groups, n)
		case "lambda-function":
			functions = append(functions, n)
		}
	}
	return roles, users, groups, functions
}

// newTestRuleContext builds an iamRuleContext for one account from a fixture.
// Convenience helper used by every rule test.
//
//nolint:unparam // account kept on signature for future cross-account tests
func newTestRuleContext(t *testing.T, fx *cloudFixture, account string) *iamRuleContext {
	t.Helper()
	roles, users, groups, functions := loadPrincipals(t, fx, account)
	return &iamRuleContext{
		caller:    fx,
		scoped:    fx.reader(account),
		Account:   account,
		Roles:     roles,
		Users:     users,
		Groups:    groups,
		Functions: functions,
	}
}

// addLambdaFunction creates a lambda-function node and (when executionRoleARN
// is non-empty) wires an EdgeAssumesRole edge from the function to its
// execution role.
//
//nolint:unparam // account kept on signature for future cross-account tests
func addLambdaFunction(t *testing.T, fx *cloudFixture, account, arn, name, executionRoleARN string) *knowledgev1.Node {
	t.Helper()
	fn := fx.AddCloudResource(account, arn, name, "lambda-function", nil)
	if executionRoleARN != "" {
		fx.AddEdge(account, arn, executionRoleARN, kgtypes.EdgeAssumesRole)
	}
	return fn
}
