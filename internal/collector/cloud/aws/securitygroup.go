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

type securityGroupCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newSecurityGroupCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &securityGroupCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *securityGroupCollector) Name() string { return "security-group" }

func (c *securityGroupCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeSecurityGroupsPaginator(c.client, &ec2.DescribeSecurityGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("security-group: describe security groups: %w", err)
		}

		for _, sg := range page.SecurityGroups {
			content, err := json.Marshal(sg)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("security-group: marshal: %w", err)
			}

			sgID := awssdk.ToString(sg.GroupId)
			sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
			sgName := awssdk.ToString(sg.GroupName)
			if sgName == "" {
				sgName = sgID
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           sgARN,
				Name:         sgName,
				ResourceType: "security-group",
				Region:       c.region,
				Content:      content,
				Metadata:     securityGroupMetadata(sg),
			})

			// SecurityGroup → VPC
			if sg.VpcId != nil {
				vpcARN := ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(sg.VpcId))
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     sgARN,
					TargetID:     vpcARN,
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

// securityGroupMetadata extracts discriminating fields from a security group.
func securityGroupMetadata(sg ec2types.SecurityGroup) map[string]string {
	m := make(map[string]string, 2)
	if v := awssdk.ToString(sg.VpcId); v != "" {
		m["vpc_id"] = v
	}
	if d := awssdk.ToString(sg.Description); d != "" {
		m["description"] = d
	}
	return m
}
