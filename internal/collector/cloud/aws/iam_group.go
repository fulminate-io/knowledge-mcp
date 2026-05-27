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

// collectGroups paginates IAM groups, emits an iam-group ResourceSpec for each
// (with inline policies attached as metadata), and returns EdgeGrants edges
// for every attached managed policy.
func (c *iamCollector) collectGroups(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := iam.NewListGroupsPaginator(c.client, &iam.ListGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("iam: list groups: %w", err)
		}

		for _, group := range page.Groups {
			content, err := json.Marshal(group)
			if err != nil {
				return nil, nil, fmt.Errorf("iam: marshal group: %w", err)
			}

			groupARN := awssdk.ToString(group.Arn)
			groupName := awssdk.ToString(group.GroupName)

			inline, err := c.collectGroupInlinePolicies(ctx, groupName)
			if err != nil {
				return nil, nil, err
			}
			meta := inline

			resources = append(resources, cloud.ResourceSpec{
				ID:           groupARN,
				Name:         groupName,
				ResourceType: "iam-group",
				Content:      content,
				Metadata:     meta,
			})

			attached, err := c.collectGroupPolicyAttachments(ctx, groupARN, groupName)
			if err != nil {
				return nil, nil, err
			}
			edges = append(edges, attached...)

			members, err := c.collectGroupMembers(ctx, groupARN, groupName)
			if err != nil {
				return nil, nil, err
			}
			edges = append(edges, members...)
		}
	}

	return resources, edges, nil
}

// collectGroupMembers returns one EdgeHasMember edge per (group, user) pair,
// emitted as group → user. This is the forward direction of EdgeMemberOf and
// lets topology analyzers walk group membership without a reverse-edge query.
// AWS GetGroup paginates the user list; we exhaust the paginator.
func (c *iamCollector) collectGroupMembers(ctx context.Context, groupARN, groupName string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := iam.NewGetGroupPaginator(c.client, &iam.GetGroupInput{
		GroupName: awssdk.String(groupName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: get group %s: %w", groupName, err)
		}

		for _, user := range page.Users {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     groupARN,
				TargetID:     awssdk.ToString(user.Arn),
				Relationship: kgtypes.EdgeHasMember,
			})
		}
	}

	return edges, nil
}

// collectGroupPolicyAttachments returns EdgeGrants edges from a group to each
// attached managed policy.
func (c *iamCollector) collectGroupPolicyAttachments(ctx context.Context, groupARN, groupName string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := iam.NewListAttachedGroupPoliciesPaginator(c.client, &iam.ListAttachedGroupPoliciesInput{
		GroupName: awssdk.String(groupName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list attached policies for group %s: %w", groupName, err)
		}

		for _, policy := range page.AttachedPolicies {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     groupARN,
				TargetID:     awssdk.ToString(policy.PolicyArn),
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"policy_name": awssdk.ToString(policy.PolicyName)},
			})
		}
	}

	return edges, nil
}

// collectGroupInlinePolicies returns a metadata map with one inline_policy_<name>
// entry per inline policy attached to the group.
func (c *iamCollector) collectGroupInlinePolicies(ctx context.Context, groupName string) (map[string]string, error) {
	var meta map[string]string

	paginator := iam.NewListGroupPoliciesPaginator(c.client, &iam.ListGroupPoliciesInput{
		GroupName: awssdk.String(groupName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list inline policies for group %s: %w", groupName, err)
		}

		for _, name := range page.PolicyNames {
			out, err := c.client.GetGroupPolicy(ctx, &iam.GetGroupPolicyInput{
				GroupName:  awssdk.String(groupName),
				PolicyName: awssdk.String(name),
			})
			if err != nil {
				return nil, fmt.Errorf("iam: get inline policy %s on group %s: %w", name, groupName, err)
			}
			if meta == nil {
				meta = make(map[string]string)
			}
			meta[inlinePolicyKey(name)] = awssdk.ToString(out.PolicyDocument)
		}
	}

	return meta, nil
}
