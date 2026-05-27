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

type networkACLCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newNetworkACLCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &networkACLCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *networkACLCollector) Name() string { return "network-acl" }

func (c *networkACLCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeNetworkAclsPaginator(c.client, &ec2.DescribeNetworkAclsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("network-acl: describe network acls: %w", err)
		}
		for _, nacl := range page.NetworkAcls {
			content, err := json.Marshal(nacl)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("network-acl: marshal: %w", err)
			}
			naclID := awssdk.ToString(nacl.NetworkAclId)
			if naclID == "" {
				continue
			}
			naclARN := ec2ARN(c.region, c.accountID, "network-acl", naclID)
			resources = append(resources, cloud.ResourceSpec{
				ID:           naclARN,
				Name:         nameTag(nacl.Tags, naclID),
				ResourceType: "network-acl",
				Region:       c.region,
				Content:      content,
				Metadata:     networkACLMetadata(nacl),
			})
			edges = append(edges, networkACLLocalEdges(c.region, c.accountID, naclARN, nacl)...)
		}
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// networkACLLocalEdges emits the edges discoverable from the SDK response
// without any cross-resource lookup: NACL → VPC (USES_NETWORK) and one
// subnet → NACL (ASSOCIATED_WITH_SUBNET) per association. Subnet-scoped
// ALLOWS edges from rule entries still live in postpopulate_networkacl.go
// because they require CIDR sentinel writes.
func networkACLLocalEdges(region, accountID, naclARN string, n ec2types.NetworkAcl) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	if v := awssdk.ToString(n.VpcId); v != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     naclARN,
			TargetID:     ec2ARN(region, accountID, "vpc", v),
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}
	for _, assoc := range n.Associations {
		subnetID := awssdk.ToString(assoc.SubnetId)
		if subnetID == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     ec2ARN(region, accountID, "subnet", subnetID),
			TargetID:     naclARN,
			Relationship: kgtypes.EdgeAssociatedWithSubnet,
		})
	}
	return edges
}

// networkACLMetadata extracts discriminating fields from a network ACL.
func networkACLMetadata(n ec2types.NetworkAcl) map[string]string {
	m := make(map[string]string, 2)
	if v := awssdk.ToString(n.VpcId); v != "" {
		m["vpc_id"] = v
	}
	if n.IsDefault != nil {
		m["is_default"] = fmt.Sprintf("%t", awssdk.ToBool(n.IsDefault))
	}
	return m
}
