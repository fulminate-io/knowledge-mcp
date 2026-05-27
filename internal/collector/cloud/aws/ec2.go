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

type ec2Collector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newEC2Collector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &ec2Collector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *ec2Collector) Name() string { return "ec2" }

func (c *ec2Collector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeInstancesPaginator(c.client, &ec2.DescribeInstancesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("ec2: describe instances: %w", err)
		}

		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				content, err := json.Marshal(instance) //nolint:gosec // G117: ClientToken is an idempotency token, not a secret
				if err != nil {
					return cloud.SubCollectorResult{}, fmt.Errorf("ec2: marshal: %w", err)
				}

				instanceID := awssdk.ToString(instance.InstanceId)
				instanceARN := ec2ARN(c.region, c.accountID, "instance", instanceID)

				resources = append(resources, cloud.ResourceSpec{
					ID:           instanceARN,
					Name:         nameTag(instance.Tags, instanceID),
					ResourceType: "ec2-instance",
					Region:       c.region,
					Content:      content,
					Metadata:     ec2InstanceMetadata(instance),
				})

				// Instance → Subnet
				if instance.SubnetId != nil {
					subnetARN := ec2ARN(c.region, c.accountID, "subnet", awssdk.ToString(instance.SubnetId))
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     instanceARN,
						TargetID:     subnetARN,
						Relationship: kgtypes.EdgeUsesSubnet,
					})
				}

				// Instance → Security Groups
				for _, sg := range instance.SecurityGroups {
					sgARN := ec2ARN(c.region, c.accountID, "security-group", awssdk.ToString(sg.GroupId))
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     instanceARN,
						TargetID:     sgARN,
						Relationship: kgtypes.EdgeUsesSecurityGroup,
					})
				}

				// Instance → IAM Role (via instance profile)
				if instance.IamInstanceProfile != nil && instance.IamInstanceProfile.Arn != nil {
					// IamInstanceProfile provides the profile ARN, not the role ARN.
					// We link to the profile ARN as an ASSUMES_ROLE relationship.
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     instanceARN,
						TargetID:     awssdk.ToString(instance.IamInstanceProfile.Arn),
						Relationship: kgtypes.EdgeAssumesRole,
						Metadata:     map[string]string{"role_source": "instance_profile"},
					})
				}
			}
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// ec2InstanceMetadata extracts discriminating fields from an EC2 instance into
// the snake_case metadata map consumed by the summarize helper. Empty values
// are omitted so downstream queries never see e.g. vpc_id="" placeholders.
// Edge-target IDs (subnet/SG/IAM) are NOT duplicated -- edges are canonical.
func ec2InstanceMetadata(inst ec2types.Instance) map[string]string {
	m := make(map[string]string, 6)
	if it := string(inst.InstanceType); it != "" {
		m["instance_type"] = it
	}
	if inst.State != nil {
		if s := string(inst.State.Name); s != "" {
			m["state"] = s
		}
	}
	if inst.Placement != nil {
		if az := awssdk.ToString(inst.Placement.AvailabilityZone); az != "" {
			m["availability_zone"] = az
		}
	}
	if vpc := awssdk.ToString(inst.VpcId); vpc != "" {
		m["vpc_id"] = vpc
	}
	if ip := awssdk.ToString(inst.PrivateIpAddress); ip != "" {
		m["private_ip"] = ip
	}
	return m
}
