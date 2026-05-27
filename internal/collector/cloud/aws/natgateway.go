// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type natGatewayCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

// newNATGatewayCollector creates a NAT Gateway subcollector.
// NAT Gateways use the EC2 API (DescribeNatGateways), like EBS.
func newNATGatewayCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &natGatewayCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *natGatewayCollector) Name() string { return "nat-gateway" }

func (c *natGatewayCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeNatGatewaysPaginator(c.client, &ec2.DescribeNatGatewaysInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("nat-gateway: describe nat gateways: %w", err)
		}

		for _, natGw := range page.NatGateways {
			content, err := json.Marshal(natGw)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("nat-gateway: marshal: %w", err)
			}

			natGwID := awssdk.ToString(natGw.NatGatewayId)
			natGwARN := ec2ARN(c.region, c.accountID, "natgateway", natGwID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           natGwARN,
				Name:         nameTag(natGw.Tags, natGwID),
				ResourceType: "nat-gateway",
				Region:       c.region,
				Content:      content,
				Metadata:     natGatewayMetadata(natGw),
			})

			// NAT Gateway → Subnet
			if subnetID := awssdk.ToString(natGw.SubnetId); subnetID != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     natGwARN,
					TargetID:     ec2ARN(c.region, c.accountID, "subnet", subnetID),
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}

			// NAT Gateway → VPC
			if vpcID := awssdk.ToString(natGw.VpcId); vpcID != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     natGwARN,
					TargetID:     ec2ARN(c.region, c.accountID, "vpc", vpcID),
					Relationship: kgtypes.EdgeUsesNetwork,
				})
			}
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// natGatewayMetadata extracts discriminating fields from a NAT gateway.
func natGatewayMetadata(n ec2types.NatGateway) map[string]string {
	m := make(map[string]string, 4)
	if s := string(n.State); s != "" {
		m["state"] = s
	}
	if c := string(n.ConnectivityType); c != "" {
		m["connectivity_type"] = c
	}
	if v := awssdk.ToString(n.SubnetId); v != "" {
		m["subnet_id"] = v
	}
	if v := awssdk.ToString(n.VpcId); v != "" {
		m["vpc_id"] = v
	}
	return m
}
