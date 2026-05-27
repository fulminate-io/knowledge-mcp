// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type redshiftCollector struct {
	client    *redshift.Client
	region    string
	accountID string
}

func newRedshiftCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &redshiftCollector{
		client:    redshift.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *redshiftCollector) Name() string { return "redshift" }

func (c *redshiftCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// Cache subnet group resolution per collect call: a single subnet group
	// is typically shared across many clusters, so memoizing avoids repeated
	// DescribeClusterSubnetGroups calls for the same name.
	subnetGroupCache := map[string][]string{}

	paginator := redshift.NewDescribeClustersPaginator(c.client, &redshift.DescribeClustersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("redshift: describe clusters: %w", err)
		}

		for _, cluster := range page.Clusters {
			content, err := json.Marshal(cluster)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("redshift: marshal: %w", err)
			}

			clusterID := awssdk.ToString(cluster.ClusterIdentifier)
			if clusterID == "" {
				continue
			}
			// Redshift does not expose a full cluster ARN on the Cluster
			// object — synthesize it from region/account/identifier.
			clusterARN := redshiftClusterARN(c.region, c.accountID, clusterID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           clusterARN,
				Name:         clusterID,
				ResourceType: "redshift-cluster",
				Region:       c.region,
				Content:      content,
				Metadata:     redshiftClusterMetadata(cluster),
			})

			edges = append(edges, c.clusterEdges(ctx, clusterARN, cluster, subnetGroupCache)...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// clusterEdges extracts all edges for a single Redshift cluster: VPC, subnets,
// security groups, IAM roles, and KMS encryption key.
func (c *redshiftCollector) clusterEdges(
	ctx context.Context,
	clusterARN string,
	cluster redshifttypes.Cluster,
	subnetGroupCache map[string][]string,
) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Redshift → VPC (USES_NETWORK)
	if cluster.VpcId != nil {
		vpcARN := ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(cluster.VpcId))
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     clusterARN,
			TargetID:     vpcARN,
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}

	// Redshift → Subnets (resolved via DescribeClusterSubnetGroups)
	if cluster.ClusterSubnetGroupName != nil {
		groupName := awssdk.ToString(cluster.ClusterSubnetGroupName)
		if groupName != "" {
			subnetIDs := c.resolveSubnetGroup(ctx, groupName, subnetGroupCache)
			for _, subnetID := range subnetIDs {
				subnetARN := ec2ARN(c.region, c.accountID, "subnet", subnetID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     clusterARN,
					TargetID:     subnetARN,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	// Redshift → VPC Security Groups
	for _, sg := range cluster.VpcSecurityGroups {
		if sg.VpcSecurityGroupId != nil {
			sgARN := ec2ARN(c.region, c.accountID, "security-group", awssdk.ToString(sg.VpcSecurityGroupId))
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     clusterARN,
				TargetID:     sgARN,
				Relationship: kgtypes.EdgeUsesSecurityGroup,
			})
		}
	}

	// Redshift → IAM Roles (COPY/UNLOAD to S3, Glue catalog, etc.)
	for _, role := range cluster.IamRoles {
		if role.IamRoleArn != nil {
			meta := map[string]string{"role_source": "cluster_iam_role"}
			if role.ApplyStatus != nil {
				meta["apply_status"] = *role.ApplyStatus
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     clusterARN,
				TargetID:     awssdk.ToString(role.IamRoleArn),
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     meta,
			})
		}
	}

	// Redshift → KMS key (at-rest encryption)
	if cluster.KmsKeyId != nil {
		kmsARN := resolveKMSKeyARN(awssdk.ToString(cluster.KmsKeyId), c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     clusterARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	return edges
}

// resolveSubnetGroup returns the subnet IDs for a named Redshift cluster
// subnet group, caching results for the lifetime of a single Collect() call.
// On API failure the cache entry is left empty so subsequent clusters sharing
// the group don't retry — partial results are preferred over blocking the
// whole collection.
func (c *redshiftCollector) resolveSubnetGroup(
	ctx context.Context,
	groupName string,
	cache map[string][]string,
) []string {
	if ids, ok := cache[groupName]; ok {
		return ids
	}

	out, err := c.client.DescribeClusterSubnetGroups(ctx, &redshift.DescribeClusterSubnetGroupsInput{
		ClusterSubnetGroupName: awssdk.String(groupName),
	})
	if err != nil {
		cache[groupName] = nil
		return nil
	}

	var ids []string
	for _, group := range out.ClusterSubnetGroups {
		for _, subnet := range group.Subnets {
			if subnet.SubnetIdentifier != nil {
				ids = append(ids, awssdk.ToString(subnet.SubnetIdentifier))
			}
		}
	}
	cache[groupName] = ids
	return ids
}

// redshiftClusterARN constructs the canonical ARN for a Redshift cluster.
// Redshift's DescribeClusters API does not include an ARN on the Cluster
// struct, so we build it from the well-known format.
func redshiftClusterARN(region, accountID, clusterID string) string {
	return fmt.Sprintf("arn:aws:redshift:%s:%s:cluster:%s", region, accountID, clusterID)
}

// redshiftClusterMetadata extracts discriminating fields from a Redshift cluster.
func redshiftClusterMetadata(c redshifttypes.Cluster) map[string]string {
	m := make(map[string]string, 3)
	if t := awssdk.ToString(c.NodeType); t != "" {
		m["node_type"] = t
	}
	if c.NumberOfNodes != nil {
		m["number_of_nodes"] = fmt.Sprintf("%d", awssdk.ToInt32(c.NumberOfNodes))
	}
	if s := awssdk.ToString(c.ClusterStatus); s != "" {
		m["status"] = s
	}
	return m
}
