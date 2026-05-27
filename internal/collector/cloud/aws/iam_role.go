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

// collectRoles paginates IAM roles, emits an iam-role ResourceSpec for each,
// attaches inline policies as inline_policy_<name> metadata, captures the
// permissions boundary (if any) under permission_boundary metadata, and
// returns EdgeGrants edges for every attached managed policy.
func (c *iamCollector) collectRoles(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	boundaries := newBoundaryCache()

	paginator := iam.NewListRolesPaginator(c.client, &iam.ListRolesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("iam: list roles: %w", err)
		}

		for _, role := range page.Roles {
			content, err := json.Marshal(role)
			if err != nil {
				return nil, nil, fmt.Errorf("iam: marshal role: %w", err)
			}

			roleARN := awssdk.ToString(role.Arn)
			roleName := awssdk.ToString(role.RoleName)

			meta, err := c.collectRoleInlinePolicies(ctx, roleName)
			if err != nil {
				return nil, nil, err
			}

			// Permissions boundary collection — fail-open. If GetPolicy or
			// GetPolicyVersion errors out, the principal is still collected
			// without the boundary field (the topology side will treat that
			// as "no restriction", which matches v1.1 behavior). The cache
			// dedupes lookups across roles that share a boundary policy.
			if role.PermissionsBoundary != nil {
				arn := awssdk.ToString(role.PermissionsBoundary.PermissionsBoundaryArn)
				if doc := boundaries.fetchBoundaryDocument(ctx, c.client, arn); doc != "" {
					meta = applyBoundaryMetadata(meta, arn, doc)
				}
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           roleARN,
				Name:         roleName,
				ResourceType: "iam-role",
				Content:      content,
				Metadata:     meta,
			})

			attachedEdges, err := c.collectRolePolicyAttachments(ctx, roleARN, roleName)
			if err != nil {
				return nil, nil, err
			}
			edges = append(edges, attachedEdges...)
		}
	}

	return resources, edges, nil
}

// collectRolePolicyAttachments returns EdgeGrants edges from a role to each
// of its attached managed policies.
func (c *iamCollector) collectRolePolicyAttachments(ctx context.Context, roleARN, roleName string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := iam.NewListAttachedRolePoliciesPaginator(c.client, &iam.ListAttachedRolePoliciesInput{
		RoleName: awssdk.String(roleName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list attached policies for role %s: %w", roleName, err)
		}

		for _, policy := range page.AttachedPolicies {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     roleARN,
				TargetID:     awssdk.ToString(policy.PolicyArn),
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"policy_name": awssdk.ToString(policy.PolicyName)},
			})
		}
	}

	return edges, nil
}

// collectRoleInlinePolicies enumerates inline policies on a role and returns
// a metadata map with one inline_policy_<name> entry per inline policy. The
// value is the URL-encoded JSON document as returned by IAM (the parser will
// URL-decode it). Returns nil if the role has no inline policies.
func (c *iamCollector) collectRoleInlinePolicies(ctx context.Context, roleName string) (map[string]string, error) {
	var meta map[string]string

	paginator := iam.NewListRolePoliciesPaginator(c.client, &iam.ListRolePoliciesInput{
		RoleName: awssdk.String(roleName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list inline policies for role %s: %w", roleName, err)
		}

		for _, name := range page.PolicyNames {
			out, err := c.client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
				RoleName:   awssdk.String(roleName),
				PolicyName: awssdk.String(name),
			})
			if err != nil {
				return nil, fmt.Errorf("iam: get inline policy %s on role %s: %w", name, roleName, err)
			}
			if meta == nil {
				meta = make(map[string]string)
			}
			meta[inlinePolicyKey(name)] = awssdk.ToString(out.PolicyDocument)
		}
	}

	return meta, nil
}

// inlinePolicyKey is the metadata key under which an inline policy document is
// stored on a principal node. The Phase 1 IAM parser scans node metadata for
// this prefix.
func inlinePolicyKey(name string) string {
	return "inline_policy_" + name
}
