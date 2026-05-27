// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/api/vpcaccess/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestVPCConnectorSubCollector_Name(t *testing.T) {
	c := &vpcConnectorSubCollector{}
	assert.Equal(t, "gcp-vpc-connectors", c.Name())
}

func TestVPCConnectorEdges_Network(t *testing.T) {
	conn := &vpcaccess.Connector{
		Name:    "projects/my-project/locations/us-central1/connectors/my-conn",
		Network: "my-network",
	}
	edges := vpcConnectorEdges("my-project", conn)
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)
	assert.Equal(t, conn.Name, edges[0].SourceID)
	assert.Contains(t, edges[0].TargetID, "networks/my-network")
}

func TestVPCConnectorEdges_Subnet(t *testing.T) {
	conn := &vpcaccess.Connector{
		Name:    "projects/my-project/locations/us-central1/connectors/my-conn",
		Network: "my-network",
		Subnet: &vpcaccess.Subnet{
			Name: "my-subnet",
		},
	}
	edges := vpcConnectorEdges("my-project", conn)
	assert.Len(t, edges, 2)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeUsesSubnet, edges[1].Relationship)
	assert.Contains(t, edges[1].TargetID, "subnetworks/my-subnet")
	assert.Contains(t, edges[1].TargetID, "us-central1")
}

func TestVPCConnectorEdges_NoEdges(t *testing.T) {
	conn := &vpcaccess.Connector{
		Name: "projects/my-project/locations/us-central1/connectors/my-conn",
	}
	edges := vpcConnectorEdges("my-project", conn)
	assert.Empty(t, edges)
}

func TestVPCConnectorEdges_SubnetCrossProject(t *testing.T) {
	conn := &vpcaccess.Connector{
		Name:    "projects/my-project/locations/us-central1/connectors/my-conn",
		Network: "my-network",
		Subnet: &vpcaccess.Subnet{
			Name:      "shared-subnet",
			ProjectId: "host-project",
		},
	}
	edges := vpcConnectorEdges("my-project", conn)
	assert.Len(t, edges, 2)
	assert.Contains(t, edges[1].TargetID, "host-project")
	assert.Contains(t, edges[1].TargetID, "subnetworks/shared-subnet")
}
