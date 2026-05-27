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

type igwCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newIGWCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &igwCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *igwCollector) Name() string { return "internet-gateway" }

func (c *igwCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeInternetGatewaysPaginator(c.client, &ec2.DescribeInternetGatewaysInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("internet-gateway: describe: %w", err)
		}

		for _, igw := range page.InternetGateways {
			content, err := json.Marshal(igw)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("internet-gateway: marshal: %w", err)
			}

			igwID := awssdk.ToString(igw.InternetGatewayId)
			igwARN := ec2ARN(c.region, c.accountID, "internet-gateway", igwID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           igwARN,
				Name:         nameTag(igw.Tags, igwID),
				ResourceType: "internet-gateway",
				Region:       c.region,
				Content:      content,
				Metadata:     internetGatewayMetadata(igw),
			})

			// IGW → VPC (one per attachment, typically one)
			for _, att := range igw.Attachments {
				vpcID := awssdk.ToString(att.VpcId)
				if vpcID == "" {
					continue
				}
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     igwARN,
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

// internetGatewayMetadata extracts discriminating fields from an IGW. The first
// VPC attachment is recorded since IGWs are 1:1 with a VPC.
func internetGatewayMetadata(igw ec2types.InternetGateway) map[string]string {
	m := make(map[string]string, 1)
	if len(igw.Attachments) > 0 {
		if v := awssdk.ToString(igw.Attachments[0].VpcId); v != "" {
			m["attached_vpc"] = v
		}
	}
	return m
}
