// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type elasticacheCollector struct {
	client    *elasticache.Client
	region    string
	accountID string
}

func newElastiCacheCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &elasticacheCollector{
		client:    elasticache.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *elasticacheCollector) Name() string { return "elasticache" }

func (c *elasticacheCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := elasticache.NewDescribeCacheClustersPaginator(c.client,
		&elasticache.DescribeCacheClustersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("elasticache: describe cache clusters: %w", err)
		}

		for _, cluster := range page.CacheClusters {
			content, err := json.Marshal(cluster)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("elasticache: marshal: %w", err)
			}
			clusterARN := awssdk.ToString(cluster.ARN)
			if clusterARN == "" {
				// Fall back to a synthetic ARN so the node still has a
				// stable ID — skip if even the cluster ID is missing.
				id := awssdk.ToString(cluster.CacheClusterId)
				if id == "" {
					continue
				}
				clusterARN = fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s",
					c.region, c.accountID, id)
			}
			clusterName := awssdk.ToString(cluster.CacheClusterId)

			resources = append(resources, cloud.ResourceSpec{
				ID:           clusterARN,
				Name:         clusterName,
				ResourceType: "elasticache-cluster",
				Region:       c.region,
				Content:      content,
				Metadata:     elasticacheClusterMetadata(cluster),
			})

			// ElastiCache → VPC Security Groups
			for _, sg := range cluster.SecurityGroups {
				sgID := awssdk.ToString(sg.SecurityGroupId)
				if sgID == "" {
					continue
				}
				sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     clusterARN,
					TargetID:     sgARN,
					Relationship: kgtypes.EdgeUsesSecurityGroup,
				})
			}
		}
	}

	// Collect replication groups for KMS encryption keys.
	rgResources, rgEdges, err := c.collectReplicationGroups(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, rgResources...)
	edges = append(edges, rgEdges...)

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectReplicationGroups enumerates ElastiCache replication groups and emits
// KMS encryption edges.  Replication groups own the KmsKeyId field; individual
// cache clusters do not expose it.
func (c *elasticacheCollector) collectReplicationGroups(
	ctx context.Context,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := elasticache.NewDescribeReplicationGroupsPaginator(c.client,
		&elasticache.DescribeReplicationGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("elasticache: describe replication groups: %w", err)
		}
		for _, rg := range page.ReplicationGroups {
			rgARN := awssdk.ToString(rg.ARN)
			if rgARN == "" {
				continue
			}
			content, err := json.Marshal(rg)
			if err != nil {
				return nil, nil, fmt.Errorf("elasticache: marshal replication group: %w", err)
			}
			resources = append(resources, cloud.ResourceSpec{
				ID:           rgARN,
				Name:         awssdk.ToString(rg.ReplicationGroupId),
				ResourceType: "elasticache-replication-group",
				Region:       c.region,
				Content:      content,
				Metadata:     elasticacheReplicationGroupMetadata(rg),
			})

			// ReplicationGroup → KMS key (at-rest encryption)
			if rg.KmsKeyId != nil {
				kmsARN := resolveKMSKeyARN(awssdk.ToString(rg.KmsKeyId), c.region, c.accountID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     rgARN,
					TargetID:     kmsARN,
					Relationship: kgtypes.EdgeEncryptsWith,
				})
			}
		}
	}

	return resources, edges, nil
}

// elasticacheClusterMetadata extracts discriminating fields from a cluster.
func elasticacheClusterMetadata(c ectypes.CacheCluster) map[string]string {
	m := make(map[string]string, 3)
	if e := awssdk.ToString(c.Engine); e != "" {
		m["engine"] = e
	}
	if v := awssdk.ToString(c.EngineVersion); v != "" {
		m["engine_version"] = v
	}
	if t := awssdk.ToString(c.CacheNodeType); t != "" {
		m["cache_node_type"] = t
	}
	return m
}

// elasticacheReplicationGroupMetadata extracts discriminating fields.
func elasticacheReplicationGroupMetadata(r ectypes.ReplicationGroup) map[string]string {
	m := make(map[string]string, 3)
	if d := awssdk.ToString(r.Description); d != "" {
		m["description"] = d
	}
	if r.MultiAZ != "" {
		m["multi_az"] = string(r.MultiAZ)
	}
	if s := awssdk.ToString(r.Status); s != "" {
		m["status"] = s
	}
	return m
}
