// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// iamAPI is the subset of the IAM client surface used by iamCollector. Defining
// it as an interface lets us mock IAM in tests without standing up real AWS
// credentials. The concrete *iam.Client satisfies this interface, and so do all
// the SDK paginator interfaces (ListUsersAPIClient, ListGroupsAPIClient, etc.)
// because their method shapes are identical.
type iamAPI interface {
	// Roles
	ListRoles(ctx context.Context, params *iam.ListRolesInput, optFns ...func(*iam.Options)) (*iam.ListRolesOutput, error)
	ListAttachedRolePolicies(ctx context.Context, params *iam.ListAttachedRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(ctx context.Context, params *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	GetRolePolicy(ctx context.Context, params *iam.GetRolePolicyInput, optFns ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)

	// Users
	ListUsers(ctx context.Context, params *iam.ListUsersInput, optFns ...func(*iam.Options)) (*iam.ListUsersOutput, error)
	ListAttachedUserPolicies(ctx context.Context, params *iam.ListAttachedUserPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedUserPoliciesOutput, error)
	ListUserPolicies(ctx context.Context, params *iam.ListUserPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error)
	GetUserPolicy(ctx context.Context, params *iam.GetUserPolicyInput, optFns ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error)
	ListGroupsForUser(ctx context.Context, params *iam.ListGroupsForUserInput, optFns ...func(*iam.Options)) (*iam.ListGroupsForUserOutput, error)

	// Groups
	ListGroups(ctx context.Context, params *iam.ListGroupsInput, optFns ...func(*iam.Options)) (*iam.ListGroupsOutput, error)
	ListAttachedGroupPolicies(ctx context.Context, params *iam.ListAttachedGroupPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedGroupPoliciesOutput, error)
	ListGroupPolicies(ctx context.Context, params *iam.ListGroupPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListGroupPoliciesOutput, error)
	GetGroupPolicy(ctx context.Context, params *iam.GetGroupPolicyInput, optFns ...func(*iam.Options)) (*iam.GetGroupPolicyOutput, error)
	GetGroup(ctx context.Context, params *iam.GetGroupInput, optFns ...func(*iam.Options)) (*iam.GetGroupOutput, error)

	// Policies
	ListPolicies(ctx context.Context, params *iam.ListPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListPoliciesOutput, error)
	GetPolicyVersion(ctx context.Context, params *iam.GetPolicyVersionInput, optFns ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
}

type iamCollector struct {
	client    iamAPI
	accountID string
}

// newIAMCollector creates an IAM subcollector. IAM is global (no region).
func newIAMCollector(cfg awssdk.Config, accountID string) cloud.SubCollector {
	return &iamCollector{
		client:    iam.NewFromConfig(cfg),
		accountID: accountID,
	}
}

func (c *iamCollector) Name() string { return "iam" }

func (c *iamCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// Roles (and their attached + inline policies).
	roles, roleEdges, err := c.collectRoles(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, roles...)
	edges = append(edges, roleEdges...)

	// Users (and their attached + inline policies + group memberships).
	users, userEdges, err := c.collectUsers(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, users...)
	edges = append(edges, userEdges...)

	// Groups (and their attached + inline policies).
	groups, groupEdges, err := c.collectGroups(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, groups...)
	edges = append(edges, groupEdges...)

	// Customer-managed policies (with full document body).
	policies, err := c.collectPolicies(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, policies...)

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}
