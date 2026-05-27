// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fixtureCollector returns an iamCollector wired to a populated fakeIAMAPI
// covering all collection paths exercised by the tests below.
func fixtureCollector() *iamCollector {
	createDate := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	roleARN := "arn:aws:iam::111111111111:role/role-dev"
	userARN := "arn:aws:iam::111111111111:user/alice"
	groupARN := "arn:aws:iam::111111111111:group/devs"
	managedARN := "arn:aws:iam::111111111111:policy/CustomerManaged"
	boundaryARN := "arn:aws:iam::111111111111:policy/DevBoundary"

	f := &fakeIAMAPI{
		roles: []iamtypes.Role{{
			Arn:        awssdk.String(roleARN),
			RoleName:   awssdk.String("role-dev"),
			Path:       awssdk.String("/"),
			CreateDate: &createDate,
			PermissionsBoundary: &iamtypes.AttachedPermissionsBoundary{
				PermissionsBoundaryArn:  awssdk.String(boundaryARN),
				PermissionsBoundaryType: iamtypes.PermissionsBoundaryAttachmentTypePolicy,
			},
		}},
		users: []iamtypes.User{{
			Arn:        awssdk.String(userARN),
			UserName:   awssdk.String("alice"),
			Path:       awssdk.String("/"),
			CreateDate: &createDate,
			PermissionsBoundary: &iamtypes.AttachedPermissionsBoundary{
				PermissionsBoundaryArn:  awssdk.String(boundaryARN),
				PermissionsBoundaryType: iamtypes.PermissionsBoundaryAttachmentTypePolicy,
			},
		}},
		groups: []iamtypes.Group{{
			Arn:        awssdk.String(groupARN),
			GroupName:  awssdk.String("devs"),
			Path:       awssdk.String("/"),
			CreateDate: &createDate,
		}},

		rolePolicies: map[string][]iamtypes.AttachedPolicy{
			"role-dev": {{PolicyArn: awssdk.String(managedARN), PolicyName: awssdk.String("CustomerManaged")}},
		},
		userPolicies: map[string][]iamtypes.AttachedPolicy{
			"alice": {{PolicyArn: awssdk.String(managedARN), PolicyName: awssdk.String("CustomerManaged")}},
		},
		groupPolicies: map[string][]iamtypes.AttachedPolicy{
			"devs": {{PolicyArn: awssdk.String(managedARN), PolicyName: awssdk.String("CustomerManaged")}},
		},

		userGroups: map[string][]iamtypes.Group{
			"alice": {{Arn: awssdk.String(groupARN), GroupName: awssdk.String("devs")}},
		},

		groupUsers: map[string][]iamtypes.User{
			"devs": {{Arn: awssdk.String(userARN), UserName: awssdk.String("alice")}},
		},

		inlineRoleNames:  map[string][]string{"role-dev": {"RoleInline"}},
		inlineUserNames:  map[string][]string{"alice": {"UserInline"}},
		inlineGroupNames: map[string][]string{"devs": {"GroupInline"}},

		inlineRoleDocs: map[string]map[string]string{
			"role-dev": {"RoleInline": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`},
		},
		inlineUserDocs: map[string]map[string]string{
			"alice": {"UserInline": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}]}`},
		},
		inlineGroupDocs: map[string]map[string]string{
			"devs": {"GroupInline": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`},
		},

		managedPolicies: map[string]iamtypes.Policy{
			managedARN: {
				Arn:              awssdk.String(managedARN),
				PolicyName:       awssdk.String("CustomerManaged"),
				DefaultVersionId: awssdk.String("v1"),
			},
			boundaryARN: {
				Arn:              awssdk.String(boundaryARN),
				PolicyName:       awssdk.String("DevBoundary"),
				DefaultVersionId: awssdk.String("v1"),
			},
		},
		policyVersions: map[string]string{
			managedARN:  `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
			boundaryARN: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
		},
	}

	return &iamCollector{client: f, accountID: "111111111111"}
}

func TestIAMCollector_CollectsRoles(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	role := findResource(res.Resources, "iam-role", "arn:aws:iam::111111111111:role/role-dev")
	require.NotNil(t, role, "expected iam-role node")
	assert.Equal(t, "role-dev", role.Name)
	assert.NotEmpty(t, role.Content, "role content should be non-empty json")
	// EdgeGrants from role to managed policy.
	assert.True(t, hasEdge(res.Edges,
		"arn:aws:iam::111111111111:role/role-dev",
		"arn:aws:iam::111111111111:policy/CustomerManaged",
		kgtypes.EdgeGrants), "expected role→policy GRANTS edge")
}

func TestIAMCollector_CollectsIAMUsers(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	user := findResource(res.Resources, "iam-user", "arn:aws:iam::111111111111:user/alice")
	require.NotNil(t, user, "expected iam-user node")
	assert.Equal(t, "alice", user.Name)
	assert.NotEmpty(t, user.Content)
}

func TestIAMCollector_CollectsIAMGroups(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	group := findResource(res.Resources, "iam-group", "arn:aws:iam::111111111111:group/devs")
	require.NotNil(t, group, "expected iam-group node")
	assert.Equal(t, "devs", group.Name)
}

func TestIAMCollector_GroupMembershipEdges(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	assert.True(t, hasEdge(res.Edges,
		"arn:aws:iam::111111111111:user/alice",
		"arn:aws:iam::111111111111:group/devs",
		kgtypes.EdgeMemberOf), "expected user→group MEMBER_OF edge")
}

// TestIAMCollector_GroupHasMemberEdges verifies that collecting a group with
// members produces one HAS_MEMBER edge per (group, user) pair, emitted as
// group → user. This is the forward direction of EdgeMemberOf and is what
// topology/iam_rules_identity_management.go walks for group membership
// expansion (per OQ6: group → user fan-out happens in the cloud collector).
func TestIAMCollector_GroupHasMemberEdges(t *testing.T) {
	c := fixtureCollector()
	// Add a second member to the devs group so we exercise the
	// "one edge per pair" assertion with more than one pair.
	fake := c.client.(*fakeIAMAPI)
	bobARN := "arn:aws:iam::111111111111:user/bob"
	fake.groupUsers["devs"] = append(fake.groupUsers["devs"], iamtypes.User{
		Arn:      awssdk.String(bobARN),
		UserName: awssdk.String("bob"),
	})

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	groupARN := "arn:aws:iam::111111111111:group/devs"
	aliceARN := "arn:aws:iam::111111111111:user/alice"

	assert.True(t, hasEdge(res.Edges, groupARN, aliceARN, kgtypes.EdgeHasMember),
		"expected group→alice HAS_MEMBER edge")
	assert.True(t, hasEdge(res.Edges, groupARN, bobARN, kgtypes.EdgeHasMember),
		"expected group→bob HAS_MEMBER edge")

	// Exactly one HAS_MEMBER edge per (group, user) pair — no duplicates.
	var hasMemberCount int
	for _, e := range res.Edges {
		if e.Relationship == kgtypes.EdgeHasMember && e.SourceID == groupARN {
			hasMemberCount++
		}
	}
	assert.Equal(t, 2, hasMemberCount, "expected exactly 2 HAS_MEMBER edges from devs group")
}

func TestIAMCollector_RoleInlinePolicyMetadata(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	role := findResource(res.Resources, "iam-role", "arn:aws:iam::111111111111:role/role-dev")
	require.NotNil(t, role)
	doc, ok := role.Metadata["inline_policy_RoleInline"]
	require.True(t, ok, "expected inline_policy_RoleInline metadata key")
	assert.Contains(t, doc, "s3:GetObject")
}

func TestIAMCollector_UserInlinePolicyMetadata(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	user := findResource(res.Resources, "iam-user", "arn:aws:iam::111111111111:user/alice")
	require.NotNil(t, user)
	doc, ok := user.Metadata["inline_policy_UserInline"]
	require.True(t, ok, "expected inline_policy_UserInline metadata key")
	assert.Contains(t, doc, "sts:AssumeRole")
}

func TestIAMCollector_GroupInlinePolicyMetadata(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	group := findResource(res.Resources, "iam-group", "arn:aws:iam::111111111111:group/devs")
	require.NotNil(t, group)
	doc, ok := group.Metadata["inline_policy_GroupInline"]
	require.True(t, ok, "expected inline_policy_GroupInline metadata key")
	assert.Contains(t, doc, "iam:PassRole")
}

func TestIAMCollector_ManagedPolicyDocumentContent(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	policy := findResource(res.Resources, "iam-policy", "arn:aws:iam::111111111111:policy/CustomerManaged")
	require.NotNil(t, policy, "expected iam-policy node")

	var envelope managedPolicyContent
	require.NoError(t, json.Unmarshal(policy.Content, &envelope), "policy content must be managedPolicyContent JSON")
	assert.Equal(t, "CustomerManaged", envelope.PolicyName)
	assert.Equal(t, "arn:aws:iam::111111111111:policy/CustomerManaged", envelope.Arn)
	assert.Contains(t, envelope.Document, `"Action":"*"`)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(policy.Content, &raw))
	allowedKeys := map[string]struct{}{
		"arn":                {},
		"policy_name":        {},
		"default_version_id": {},
		"attachment_count":   {},
		"is_attachable":      {},
		"path":               {},
		"create_date":        {},
		"update_date":        {},
		"document":           {},
	}
	for key := range raw {
		_, ok := allowedKeys[key]
		assert.True(t, ok, "unexpected key %q in marshaled Content — possible SDK leak", key)
	}
}

func TestIAMCollector_ManagedPolicyMissingDefaultVersion(t *testing.T) {
	// Defensive: a policy without a DefaultVersionId should still be emitted,
	// just with an empty Document field.
	c := fixtureCollector()
	fake := c.client.(*fakeIAMAPI)
	for arn, p := range fake.managedPolicies {
		p.DefaultVersionId = nil
		fake.managedPolicies[arn] = p
	}

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	policy := findResource(res.Resources, "iam-policy", "arn:aws:iam::111111111111:policy/CustomerManaged")
	require.NotNil(t, policy)

	var envelope managedPolicyContent
	require.NoError(t, json.Unmarshal(policy.Content, &envelope))
	assert.Empty(t, envelope.Document)
}

// --- permission boundary collection tests ---------------------------------

const boundaryARNFixture = "arn:aws:iam::111111111111:policy/DevBoundary"

// TestIAMCollector_RolePermissionBoundaryMetadata verifies that a role with a
// PermissionsBoundary set has the boundary policy document persisted under
// the permission_boundary metadata key (and the ARN under
// permission_boundary_arn). The fixture's role-dev has DevBoundary attached.
func TestIAMCollector_RolePermissionBoundaryMetadata(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	role := findResource(res.Resources, "iam-role", "arn:aws:iam::111111111111:role/role-dev")
	require.NotNil(t, role, "expected iam-role node")

	arn, ok := role.Metadata["permission_boundary_arn"]
	require.True(t, ok, "expected permission_boundary_arn metadata")
	assert.Equal(t, boundaryARNFixture, arn)

	doc, ok := role.Metadata["permission_boundary"]
	require.True(t, ok, "expected permission_boundary metadata")
	assert.Contains(t, doc, "s3:*", "boundary doc should contain Action s3:*")
}

// TestIAMCollector_UserPermissionBoundaryMetadata verifies the same persistence
// for users.
func TestIAMCollector_UserPermissionBoundaryMetadata(t *testing.T) {
	c := fixtureCollector()

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	user := findResource(res.Resources, "iam-user", "arn:aws:iam::111111111111:user/alice")
	require.NotNil(t, user, "expected iam-user node")

	arn, ok := user.Metadata["permission_boundary_arn"]
	require.True(t, ok, "expected permission_boundary_arn metadata")
	assert.Equal(t, boundaryARNFixture, arn)

	doc, ok := user.Metadata["permission_boundary"]
	require.True(t, ok, "expected permission_boundary metadata")
	assert.Contains(t, doc, "s3:*", "boundary doc should contain Action s3:*")
}

// TestIAMCollector_RoleNoPermissionBoundary verifies the absence path: a role
// without PermissionsBoundary set must NOT have permission_boundary metadata.
func TestIAMCollector_RoleNoPermissionBoundary(t *testing.T) {
	c := fixtureCollector()
	fake := c.client.(*fakeIAMAPI)
	// Strip the boundary from every role.
	for i := range fake.roles {
		fake.roles[i].PermissionsBoundary = nil
	}

	res, err := c.Collect(context.Background())
	require.NoError(t, err)

	role := findResource(res.Resources, "iam-role", "arn:aws:iam::111111111111:role/role-dev")
	require.NotNil(t, role)
	_, hasArn := role.Metadata["permission_boundary_arn"]
	_, hasDoc := role.Metadata["permission_boundary"]
	assert.False(t, hasArn, "permission_boundary_arn must be absent when no boundary")
	assert.False(t, hasDoc, "permission_boundary must be absent when no boundary")
}

// TestIAMCollector_PermissionBoundaryFailOpen verifies the fail-open contract:
// when GetPolicy returns NoSuchEntity (e.g. boundary policy was deleted), the
// principal is still collected, just without the permission_boundary metadata.
// The collector must NOT return an error.
func TestIAMCollector_PermissionBoundaryFailOpen(t *testing.T) {
	c := fixtureCollector()
	fake := c.client.(*fakeIAMAPI)
	// Drop the boundary policy from the managedPolicies map so GetPolicy
	// returns NoSuchEntity, but leave the role's PermissionsBoundary attached.
	delete(fake.managedPolicies, boundaryARNFixture)

	res, err := c.Collect(context.Background())
	require.NoError(t, err, "fail-open: collector must not error on inaccessible boundary")

	role := findResource(res.Resources, "iam-role", "arn:aws:iam::111111111111:role/role-dev")
	require.NotNil(t, role)
	_, hasDoc := role.Metadata["permission_boundary"]
	assert.False(t, hasDoc, "permission_boundary must be absent when fetch fails")

	user := findResource(res.Resources, "iam-user", "arn:aws:iam::111111111111:user/alice")
	require.NotNil(t, user)
	_, hasUserDoc := user.Metadata["permission_boundary"]
	assert.False(t, hasUserDoc, "permission_boundary must be absent when fetch fails")
}
