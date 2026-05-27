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
)

type vpcCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newVPCCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &vpcCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *vpcCollector) Name() string { return "vpc" }

func (c *vpcCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var resources []cloud.ResourceSpec

	paginator := ec2.NewDescribeVpcsPaginator(c.client, &ec2.DescribeVpcsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("vpc: describe vpcs: %w", err)
		}

		for _, vpc := range page.Vpcs {
			content, err := json.Marshal(vpc)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("vpc: marshal: %w", err)
			}

			vpcID := awssdk.ToString(vpc.VpcId)
			resources = append(resources, cloud.ResourceSpec{
				ID:           ec2ARN(c.region, c.accountID, "vpc", vpcID),
				Name:         nameTag(vpc.Tags, vpcID),
				ResourceType: "vpc",
				Region:       c.region,
				Content:      content,
				Metadata:     vpcMetadata(vpc),
			})
		}
	}

	return cloud.SubCollectorResult{Resources: resources}, nil
}

// nameTag extracts the Name tag from a tag slice, falling back to the given default.
func nameTag(tags []ec2types.Tag, fallback string) string {
	for _, tag := range tags {
		if awssdk.ToString(tag.Key) == "Name" {
			return awssdk.ToString(tag.Value)
		}
	}
	return fallback
}

// vpcMetadata extracts discriminating fields from an EC2 VPC.
func vpcMetadata(v ec2types.Vpc) map[string]string {
	m := make(map[string]string, 3)
	if cidr := awssdk.ToString(v.CidrBlock); cidr != "" {
		m["cidr_block"] = cidr
	}
	if v.IsDefault != nil {
		m["is_default"] = fmt.Sprintf("%t", awssdk.ToBool(v.IsDefault))
	}
	if s := string(v.State); s != "" {
		m["state"] = s
	}
	return m
}
