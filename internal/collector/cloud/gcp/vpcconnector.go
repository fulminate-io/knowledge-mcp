// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/api/vpcaccess/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// vpcConnectorSubCollector collects Serverless VPC Access connectors.
// Uses the REST-based google.golang.org/api (not gRPC), same pattern as sqlSubCollector.
type vpcConnectorSubCollector struct {
	service   *vpcaccess.Service
	projectID string
}

func newVPCConnectorSubCollector(service *vpcaccess.Service, projectID string) *vpcConnectorSubCollector {
	return &vpcConnectorSubCollector{service: service, projectID: projectID}
}

func (c *vpcConnectorSubCollector) Name() string { return "gcp-vpc-connectors" }

func (c *vpcConnectorSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	parent := fmt.Sprintf("projects/%s/locations/-", c.projectID)
	err := c.service.Projects.Locations.Connectors.List(parent).Pages(ctx, func(resp *vpcaccess.ListConnectorsResponse) error {
		for _, conn := range resp.Connectors {
			if conn.Name == "" {
				continue
			}

			content, err := json.Marshal(conn)
			if err != nil {
				continue
			}

			spec := cloud.ResourceSpec{
				ID:           conn.Name,
				Name:         extractLast(conn.Name),
				ResourceType: "gcp:vpcaccess:connector",
				Region:       extractLocationFromName(conn.Name),
				Content:      content,
				Metadata: map[string]string{
					"network":       conn.Network,
					"ipCidrRange":   conn.IpCidrRange,
					"minThroughput": strconv.FormatInt(conn.MinThroughput, 10),
					"maxThroughput": strconv.FormatInt(conn.MaxThroughput, 10),
					"state":         conn.State,
				},
			}
			result.Resources = append(result.Resources, spec)
			result.Edges = append(result.Edges, vpcConnectorEdges(c.projectID, conn)...)
		}
		return nil
	})

	return result, err
}

// vpcConnectorEdges extracts network and subnet edges from a VPC connector.
func vpcConnectorEdges(projectID string, conn *vpcaccess.Connector) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Connector -> VPC network.
	if conn.Network != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     conn.Name,
			TargetID:     computeSelfLink(projectID, "networks", conn.Network),
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}

	// Connector -> subnet (when configured with a subnet instead of IP range).
	if conn.Subnet != nil && conn.Subnet.Name != "" {
		pid := conn.Subnet.ProjectId
		if pid == "" {
			pid = projectID
		}
		region := extractLocationFromName(conn.Name)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     conn.Name,
			TargetID:     computeRegionalSelfLink(pid, region, "subnetworks", conn.Subnet.Name),
			Relationship: kgtypes.EdgeUsesSubnet,
		})
	}

	return edges
}
