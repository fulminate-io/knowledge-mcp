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

type vpcEndpointCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newVpcEndpointCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &vpcEndpointCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *vpcEndpointCollector) Name() string { return "vpc-endpoint" }

func (c *vpcEndpointCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeVpcEndpointsPaginator(c.client, &ec2.DescribeVpcEndpointsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("vpc-endpoint: describe: %w", err)
		}
		for _, ep := range page.VpcEndpoints {
			res, epEdges, err := c.buildVpcEndpointNode(ep)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			if res == nil {
				continue
			}
			resources = append(resources, *res)
			edges = append(edges, epEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// buildVpcEndpointNode serializes one VpcEndpoint into the resource
// spec the collector publishes plus every edge implied by its VPC,
// security groups, subnets, and service name. Returns (nil, nil, nil)
// when the endpoint carries no ID.
func (c *vpcEndpointCollector) buildVpcEndpointNode(ep ec2types.VpcEndpoint) (*cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	content, err := json.Marshal(ep)
	if err != nil {
		return nil, nil, fmt.Errorf("vpc-endpoint: marshal: %w", err)
	}
	epID := awssdk.ToString(ep.VpcEndpointId)
	if epID == "" {
		return nil, nil, nil
	}
	epARN := ec2ARN(c.region, c.accountID, "vpc-endpoint", epID)
	res := cloud.ResourceSpec{
		ID:           epARN,
		Name:         nameTag(ep.Tags, epID),
		ResourceType: "vpc-endpoint",
		Region:       c.region,
		Content:      content,
		Metadata:     vpcEndpointMetadata(ep),
	}
	var edges []cloud.EdgeSpec

	// Endpoint → VPC (the endpoint lives in this VPC).
	if ep.VpcId != nil {
		vpcARN := ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(ep.VpcId))
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     epARN,
			TargetID:     vpcARN,
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}
	edges = append(edges, c.vpcEndpointSecurityGroupEdges(epARN, ep.Groups)...)
	edges = append(edges, c.vpcEndpointSubnetEdges(epARN, ep.SubnetIds)...)

	// EdgeExposedVia: the service this endpoint fronts. We use the
	// service name as a sentinel node ID so downstream analyzers can
	// walk service → endpoint → VPC.
	if svc := awssdk.ToString(ep.ServiceName); svc != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     epARN,
			TargetID:     "aws:service:" + svc,
			Relationship: kgtypes.EdgeExposedVia,
		})
	}
	return &res, edges, nil
}

// vpcEndpointSecurityGroupEdges emits one EdgeUsesSecurityGroup per
// attached security group on an interface endpoint. Gateway endpoints
// pass an empty Groups slice and produce no edges.
func (c *vpcEndpointCollector) vpcEndpointSecurityGroupEdges(epARN string, groups []ec2types.SecurityGroupIdentifier) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, sg := range groups {
		sgID := awssdk.ToString(sg.GroupId)
		if sgID == "" {
			continue
		}
		sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     epARN,
			TargetID:     sgARN,
			Relationship: kgtypes.EdgeUsesSecurityGroup,
		})
	}
	return edges
}

// vpcEndpointSubnetEdges emits one EdgeUsesSubnet per non-empty subnet
// ID the endpoint is attached to.
func (c *vpcEndpointCollector) vpcEndpointSubnetEdges(epARN string, subnetIDs []string) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, subnetID := range subnetIDs {
		if subnetID == "" {
			continue
		}
		subnetARN := ec2ARN(c.region, c.accountID, "subnet", subnetID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     epARN,
			TargetID:     subnetARN,
			Relationship: kgtypes.EdgeUsesSubnet,
		})
	}
	return edges
}

// vpcEndpointMetadata extracts discriminating fields from a VPC endpoint.
func vpcEndpointMetadata(ep ec2types.VpcEndpoint) map[string]string {
	m := make(map[string]string, 4)
	if vpc := awssdk.ToString(ep.VpcId); vpc != "" {
		m["vpc_id"] = vpc
	}
	if svc := awssdk.ToString(ep.ServiceName); svc != "" {
		m["service_name"] = svc
	}
	if t := string(ep.VpcEndpointType); t != "" {
		m["vpc_endpoint_type"] = t
	}
	if s := string(ep.State); s != "" {
		m["state"] = s
	}
	return m
}
