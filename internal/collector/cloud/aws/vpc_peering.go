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

type vpcPeeringCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newVpcPeeringCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &vpcPeeringCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *vpcPeeringCollector) Name() string { return "vpc-peering" }

func (c *vpcPeeringCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeVpcPeeringConnectionsPaginator(c.client, &ec2.DescribeVpcPeeringConnectionsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("vpc-peering: describe: %w", err)
		}
		for _, conn := range page.VpcPeeringConnections {
			content, err := json.Marshal(conn)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("vpc-peering: marshal: %w", err)
			}
			id := awssdk.ToString(conn.VpcPeeringConnectionId)
			if id == "" {
				continue
			}
			peeringARN := ec2ARN(c.region, c.accountID, "vpc-peering-connection", id)
			resources = append(resources, cloud.ResourceSpec{
				ID:           peeringARN,
				Name:         nameTag(conn.Tags, id),
				ResourceType: "vpc-peering-connection",
				Region:       c.region,
				Content:      content,
				Metadata:     vpcPeeringMetadata(conn),
			})
			edges = append(edges, vpcPeeringLocalEdges(c.region, c.accountID, peeringARN, conn)...)
		}
	}
	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// vpcPeeringLocalEdges emits peering→VPC link edges for active peerings.
// The bidirectional EdgePeeredWith between the two VPCs is still produced by
// postpopulate_crossvpc.go, where cross-account VPC validation happens; this
// just makes the peering node connected at collect-time so traversal between
// Collect and PostPopulate sees a real graph instead of orphan peering nodes.
// Cross-account VPC ARNs use the peer's OwnerId when present.
func vpcPeeringLocalEdges(region, localAccount, peeringARN string, conn ec2types.VpcPeeringConnection) []cloud.EdgeSpec {
	if conn.Status == nil || string(conn.Status.Code) != "active" {
		return nil
	}
	var edges []cloud.EdgeSpec
	if v := vpcPeerVpcARN(region, localAccount, conn.RequesterVpcInfo); v != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     peeringARN,
			TargetID:     v,
			Relationship: kgtypes.EdgeUsesNetwork,
			Metadata:     map[string]string{"role": "requester"},
		})
	}
	if v := vpcPeerVpcARN(region, localAccount, conn.AccepterVpcInfo); v != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     peeringARN,
			TargetID:     v,
			Relationship: kgtypes.EdgeUsesNetwork,
			Metadata:     map[string]string{"role": "accepter"},
		})
	}
	return edges
}

func vpcPeerVpcARN(region, localAccount string, info *ec2types.VpcPeeringConnectionVpcInfo) string {
	if info == nil {
		return ""
	}
	vpcID := awssdk.ToString(info.VpcId)
	if vpcID == "" {
		return ""
	}
	owner := awssdk.ToString(info.OwnerId)
	if owner == "" {
		owner = localAccount
	}
	return ec2ARN(region, owner, "vpc", vpcID)
}

// vpcPeeringMetadata extracts discriminating fields from a VPC peering connection.
func vpcPeeringMetadata(conn ec2types.VpcPeeringConnection) map[string]string {
	m := make(map[string]string, 3)
	if conn.Status != nil {
		if c := string(conn.Status.Code); c != "" {
			m["status"] = c
		}
	}
	if conn.RequesterVpcInfo != nil {
		if v := awssdk.ToString(conn.RequesterVpcInfo.VpcId); v != "" {
			m["requester_vpc_id"] = v
		}
	}
	if conn.AccepterVpcInfo != nil {
		if v := awssdk.ToString(conn.AccepterVpcInfo.VpcId); v != "" {
			m["accepter_vpc_id"] = v
		}
	}
	return m
}
