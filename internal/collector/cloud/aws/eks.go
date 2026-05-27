// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type eksCollector struct {
	client    *eks.Client
	region    string
	accountID string
}

func newEKSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &eksCollector{
		client:    eks.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *eksCollector) Name() string { return "eks" }

func (c *eksCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
		targets   []cloud.CollectTarget
	)

	// EKS ListClusters returns only cluster names — must DescribeCluster each.
	paginator := eks.NewListClustersPaginator(c.client, &eks.ListClustersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("eks: list clusters: %w", err)
		}

		for _, clusterName := range page.Clusters {
			desc, err := c.client.DescribeCluster(ctx, &eks.DescribeClusterInput{
				Name: awssdk.String(clusterName),
			})
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("eks: describe cluster %s: %w", clusterName, err)
			}

			cluster := desc.Cluster
			content, err := json.Marshal(cluster)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("eks: marshal: %w", err)
			}

			clusterARN := awssdk.ToString(cluster.Arn)

			meta := map[string]string{}
			if cluster.Identity != nil && cluster.Identity.Oidc != nil &&
				cluster.Identity.Oidc.Issuer != nil {
				meta["oidc_issuer"] = awssdk.ToString(cluster.Identity.Oidc.Issuer)
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           clusterARN,
				Name:         clusterName,
				ResourceType: "eks-cluster",
				Region:       c.region,
				Content:      content,
				Metadata:     meta,
			})

			// EKS → IAM Role
			if cluster.RoleArn != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     clusterARN,
					TargetID:     awssdk.ToString(cluster.RoleArn),
					Relationship: kgtypes.EdgeAssumesRole,
					Metadata:     map[string]string{"role_source": "cluster_role"},
				})
			}

			// EKS → VPC, Subnets, Security Groups
			edges = append(edges, c.vpcConfigEdges(clusterARN, cluster.ResourcesVpcConfig)...)

			// Cascade: trigger k8s collector for this cluster.
			// ID format: arn:aws:eks:region:account:cluster/name
			targets = append(targets, cloud.CollectTarget{
				Collector: "k8s",
				ID:        clusterARN,
			})
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
		Targets:   targets,
	}, nil
}

func (c *eksCollector) vpcConfigEdges(sourceARN string, vpcCfg *ekstypes.VpcConfigResponse) []cloud.EdgeSpec {
	if vpcCfg == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	if vpcCfg.VpcId != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: sourceARN, TargetID: ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(vpcCfg.VpcId)), Relationship: kgtypes.EdgeUsesNetwork,
		})
	}
	for _, subnetID := range vpcCfg.SubnetIds {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: sourceARN, TargetID: ec2ARN(c.region, c.accountID, "subnet", subnetID), Relationship: kgtypes.EdgeUsesSubnet,
		})
	}
	for _, sgID := range vpcCfg.SecurityGroupIds {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: sourceARN, TargetID: ec2ARN(c.region, c.accountID, "security-group", sgID), Relationship: kgtypes.EdgeUsesSecurityGroup,
		})
	}
	return edges
}
