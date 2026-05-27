// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeIAMAPI is a minimal in-memory IAM client used by collector unit tests.
// Each field stores the canned response for a particular API. Pagination is
// not exercised — every list returns a single page with IsTruncated=false.
type fakeIAMAPI struct {
	roles  []iamtypes.Role
	users  []iamtypes.User
	groups []iamtypes.Group

	rolePolicies  map[string][]iamtypes.AttachedPolicy
	userPolicies  map[string][]iamtypes.AttachedPolicy
	groupPolicies map[string][]iamtypes.AttachedPolicy

	userGroups map[string][]iamtypes.Group
	groupUsers map[string][]iamtypes.User

	inlineRoleNames  map[string][]string
	inlineUserNames  map[string][]string
	inlineGroupNames map[string][]string

	inlineRoleDocs  map[string]map[string]string // role -> name -> doc
	inlineUserDocs  map[string]map[string]string
	inlineGroupDocs map[string]map[string]string

	managedPolicies map[string]iamtypes.Policy // arn -> policy
	policyVersions  map[string]string          // arn -> document body
}

func (f *fakeIAMAPI) ListRoles(_ context.Context, _ *iam.ListRolesInput, _ ...func(*iam.Options)) (*iam.ListRolesOutput, error) {
	return &iam.ListRolesOutput{Roles: f.roles}, nil
}

func (f *fakeIAMAPI) ListAttachedRolePolicies(_ context.Context, in *iam.ListAttachedRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	return &iam.ListAttachedRolePoliciesOutput{
		AttachedPolicies: f.rolePolicies[awssdk.ToString(in.RoleName)],
	}, nil
}

func (f *fakeIAMAPI) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	return &iam.ListRolePoliciesOutput{
		PolicyNames: f.inlineRoleNames[awssdk.ToString(in.RoleName)],
	}, nil
}

func (f *fakeIAMAPI) GetRolePolicy(_ context.Context, in *iam.GetRolePolicyInput, _ ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	doc := f.inlineRoleDocs[awssdk.ToString(in.RoleName)][awssdk.ToString(in.PolicyName)]
	return &iam.GetRolePolicyOutput{
		RoleName:       in.RoleName,
		PolicyName:     in.PolicyName,
		PolicyDocument: awssdk.String(doc),
	}, nil
}

func (f *fakeIAMAPI) ListUsers(_ context.Context, _ *iam.ListUsersInput, _ ...func(*iam.Options)) (*iam.ListUsersOutput, error) {
	return &iam.ListUsersOutput{Users: f.users}, nil
}

func (f *fakeIAMAPI) ListAttachedUserPolicies(_ context.Context, in *iam.ListAttachedUserPoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedUserPoliciesOutput, error) {
	return &iam.ListAttachedUserPoliciesOutput{
		AttachedPolicies: f.userPolicies[awssdk.ToString(in.UserName)],
	}, nil
}

func (f *fakeIAMAPI) ListUserPolicies(_ context.Context, in *iam.ListUserPoliciesInput, _ ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error) {
	return &iam.ListUserPoliciesOutput{
		PolicyNames: f.inlineUserNames[awssdk.ToString(in.UserName)],
	}, nil
}

func (f *fakeIAMAPI) GetUserPolicy(_ context.Context, in *iam.GetUserPolicyInput, _ ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error) {
	doc := f.inlineUserDocs[awssdk.ToString(in.UserName)][awssdk.ToString(in.PolicyName)]
	return &iam.GetUserPolicyOutput{
		UserName:       in.UserName,
		PolicyName:     in.PolicyName,
		PolicyDocument: awssdk.String(doc),
	}, nil
}

func (f *fakeIAMAPI) ListGroupsForUser(_ context.Context, in *iam.ListGroupsForUserInput, _ ...func(*iam.Options)) (*iam.ListGroupsForUserOutput, error) {
	return &iam.ListGroupsForUserOutput{
		Groups: f.userGroups[awssdk.ToString(in.UserName)],
	}, nil
}

func (f *fakeIAMAPI) ListGroups(_ context.Context, _ *iam.ListGroupsInput, _ ...func(*iam.Options)) (*iam.ListGroupsOutput, error) {
	return &iam.ListGroupsOutput{Groups: f.groups}, nil
}

func (f *fakeIAMAPI) ListAttachedGroupPolicies(_ context.Context, in *iam.ListAttachedGroupPoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedGroupPoliciesOutput, error) {
	return &iam.ListAttachedGroupPoliciesOutput{
		AttachedPolicies: f.groupPolicies[awssdk.ToString(in.GroupName)],
	}, nil
}

func (f *fakeIAMAPI) ListGroupPolicies(_ context.Context, in *iam.ListGroupPoliciesInput, _ ...func(*iam.Options)) (*iam.ListGroupPoliciesOutput, error) {
	return &iam.ListGroupPoliciesOutput{
		PolicyNames: f.inlineGroupNames[awssdk.ToString(in.GroupName)],
	}, nil
}

func (f *fakeIAMAPI) GetGroupPolicy(_ context.Context, in *iam.GetGroupPolicyInput, _ ...func(*iam.Options)) (*iam.GetGroupPolicyOutput, error) {
	doc := f.inlineGroupDocs[awssdk.ToString(in.GroupName)][awssdk.ToString(in.PolicyName)]
	return &iam.GetGroupPolicyOutput{
		GroupName:      in.GroupName,
		PolicyName:     in.PolicyName,
		PolicyDocument: awssdk.String(doc),
	}, nil
}

// GetGroup returns the canned user list for a group, mirroring the AWS
// GetGroup API surface used by collectGroupMembers. Pagination is not
// exercised — every response sets IsTruncated=false.
func (f *fakeIAMAPI) GetGroup(_ context.Context, in *iam.GetGroupInput, _ ...func(*iam.Options)) (*iam.GetGroupOutput, error) {
	groupName := awssdk.ToString(in.GroupName)
	var groupShape *iamtypes.Group
	for i := range f.groups {
		if awssdk.ToString(f.groups[i].GroupName) == groupName {
			groupShape = &f.groups[i]
			break
		}
	}
	return &iam.GetGroupOutput{
		Group: groupShape,
		Users: f.groupUsers[groupName],
	}, nil
}

func (f *fakeIAMAPI) ListPolicies(_ context.Context, _ *iam.ListPoliciesInput, _ ...func(*iam.Options)) (*iam.ListPoliciesOutput, error) {
	out := make([]iamtypes.Policy, 0, len(f.managedPolicies))
	for _, p := range f.managedPolicies {
		out = append(out, p)
	}
	return &iam.ListPoliciesOutput{Policies: out}, nil
}

func (f *fakeIAMAPI) GetPolicyVersion(_ context.Context, in *iam.GetPolicyVersionInput, _ ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	doc := f.policyVersions[awssdk.ToString(in.PolicyArn)]
	return &iam.GetPolicyVersionOutput{
		PolicyVersion: &iamtypes.PolicyVersion{
			Document:         awssdk.String(doc),
			VersionId:        in.VersionId,
			IsDefaultVersion: true,
		},
	}, nil
}

// GetPolicy is the boundary-fetcher half of the API surface used by the
// permission-boundary collection in iam_role.go / iam_user.go. Tests may
// register a managed policy via managedPolicies and the fake will resolve
// its DefaultVersionId here. Returns NoSuchEntity-shaped failure (a Go
// error) when the ARN is unknown so the fail-open path is exercisable.
func (f *fakeIAMAPI) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	arn := awssdk.ToString(in.PolicyArn)
	policy, ok := f.managedPolicies[arn]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: awssdk.String("no such policy: " + arn)}
	}
	return &iam.GetPolicyOutput{Policy: &policy}, nil
}

// findResource locates the first ResourceSpec with the given resource type
// and ID. Returns nil if not found.
func findResource(resources []cloud.ResourceSpec, resourceType, id string) *cloud.ResourceSpec {
	for i := range resources {
		if resources[i].ResourceType == resourceType && resources[i].ID == id {
			return &resources[i]
		}
	}
	return nil
}

// hasEdge returns true if any edge has the given source, target and relationship.
func hasEdge(edges []cloud.EdgeSpec, source, target string, rel kgtypes.EdgeType) bool {
	for _, e := range edges {
		if e.SourceID == source && e.TargetID == target && e.Relationship == rel {
			return true
		}
	}
	return false
}
