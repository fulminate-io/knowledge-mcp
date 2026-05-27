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

type subnetCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newSubnetCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &subnetCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *subnetCollector) Name() string { return "subnet" }

func (c *subnetCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeSubnetsPaginator(c.client, &ec2.DescribeSubnetsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("subnet: describe subnets: %w", err)
		}

		for _, subnet := range page.Subnets {
			content, err := json.Marshal(subnet)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("subnet: marshal: %w", err)
			}

			subnetID := awssdk.ToString(subnet.SubnetId)
			subnetARN := ec2ARN(c.region, c.accountID, "subnet", subnetID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           subnetARN,
				Name:         nameTag(subnet.Tags, subnetID),
				ResourceType: "subnet",
				Region:       c.region,
				Content:      content,
				Metadata:     subnetMetadata(subnet),
			})

			// Subnet → VPC
			if subnet.VpcId != nil {
				vpcARN := ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(subnet.VpcId))
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     subnetARN,
					TargetID:     vpcARN,
					Relationship: kgtypes.EdgeUsesNetwork,
					Metadata: map[string]string{
						"subnet_cidr":       awssdk.ToString(subnet.CidrBlock),
						"availability_zone": awssdk.ToString(subnet.AvailabilityZone),
					},
				})
			}
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// subnetMetadata extracts discriminating fields from a subnet.
func subnetMetadata(s ec2types.Subnet) map[string]string {
	m := make(map[string]string, 4)
	if v := awssdk.ToString(s.VpcId); v != "" {
		m["vpc_id"] = v
	}
	if v := awssdk.ToString(s.CidrBlock); v != "" {
		m["cidr_block"] = v
	}
	if v := awssdk.ToString(s.AvailabilityZone); v != "" {
		m["availability_zone"] = v
	}
	if v := string(s.State); v != "" {
		m["state"] = v
	}
	return m
}
