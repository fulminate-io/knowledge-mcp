// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectUsers paginates IAM users, emits an iam-user ResourceSpec for each
// (with inline policies + permissions boundary attached as metadata), and
// returns edges for both attached managed policies (EdgeGrants) and group
// memberships (EdgeMemberOf).
func (c *iamCollector) collectUsers(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	boundaries := newBoundaryCache()

	paginator := iam.NewListUsersPaginator(c.client, &iam.ListUsersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("iam: list users: %w", err)
		}

		for _, user := range page.Users {
			content, err := json.Marshal(user)
			if err != nil {
				return nil, nil, fmt.Errorf("iam: marshal user: %w", err)
			}

			userARN := awssdk.ToString(user.Arn)
			userName := awssdk.ToString(user.UserName)

			inline, err := c.collectUserInlinePolicies(ctx, userName)
			if err != nil {
				return nil, nil, err
			}
			meta := inline

			// Permissions boundary collection — fail-open. See iam_boundary.go
			// for the rationale: collector failures must never block
			// principal collection; topology fails closed at evaluation
			// time only when a boundary IS persisted and IS parseable.
			if user.PermissionsBoundary != nil {
				arn := awssdk.ToString(user.PermissionsBoundary.PermissionsBoundaryArn)
				if doc := boundaries.fetchBoundaryDocument(ctx, c.client, arn); doc != "" {
					meta = applyBoundaryMetadata(meta, arn, doc)
				}
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           userARN,
				Name:         userName,
				ResourceType: "iam-user",
				Content:      content,
				Metadata:     meta,
			})

			attached, err := c.collectUserPolicyAttachments(ctx, userARN, userName)
			if err != nil {
				return nil, nil, err
			}
			edges = append(edges, attached...)

			memberships, err := c.collectUserGroupMemberships(ctx, userARN, userName)
			if err != nil {
				return nil, nil, err
			}
			edges = append(edges, memberships...)
		}
	}

	return resources, edges, nil
}

// collectUserPolicyAttachments returns EdgeGrants edges from a user to each
// attached managed policy.
func (c *iamCollector) collectUserPolicyAttachments(ctx context.Context, userARN, userName string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := iam.NewListAttachedUserPoliciesPaginator(c.client, &iam.ListAttachedUserPoliciesInput{
		UserName: awssdk.String(userName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list attached policies for user %s: %w", userName, err)
		}

		for _, policy := range page.AttachedPolicies {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     userARN,
				TargetID:     awssdk.ToString(policy.PolicyArn),
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"policy_name": awssdk.ToString(policy.PolicyName)},
			})
		}
	}

	return edges, nil
}

// collectUserGroupMemberships returns EdgeMemberOf edges from a user to each
// IAM group it belongs to.
func (c *iamCollector) collectUserGroupMemberships(ctx context.Context, userARN, userName string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := iam.NewListGroupsForUserPaginator(c.client, &iam.ListGroupsForUserInput{
		UserName: awssdk.String(userName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list groups for user %s: %w", userName, err)
		}

		for _, group := range page.Groups {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     userARN,
				TargetID:     awssdk.ToString(group.Arn),
				Relationship: kgtypes.EdgeMemberOf,
			})
		}
	}

	return edges, nil
}

// collectUserInlinePolicies returns a metadata map with one inline_policy_<name>
// entry per inline policy attached to the user.
func (c *iamCollector) collectUserInlinePolicies(ctx context.Context, userName string) (map[string]string, error) {
	var meta map[string]string

	paginator := iam.NewListUserPoliciesPaginator(c.client, &iam.ListUserPoliciesInput{
		UserName: awssdk.String(userName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list inline policies for user %s: %w", userName, err)
		}

		for _, name := range page.PolicyNames {
			out, err := c.client.GetUserPolicy(ctx, &iam.GetUserPolicyInput{
				UserName:   awssdk.String(userName),
				PolicyName: awssdk.String(name),
			})
			if err != nil {
				return nil, fmt.Errorf("iam: get inline policy %s on user %s: %w", name, userName, err)
			}
			if meta == nil {
				meta = make(map[string]string)
			}
			meta[inlinePolicyKey(name)] = awssdk.ToString(out.PolicyDocument)
		}
	}

	return meta, nil
}
