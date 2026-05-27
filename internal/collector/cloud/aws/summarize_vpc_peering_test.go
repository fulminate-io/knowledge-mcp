// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVPCPeeringConnection(t *testing.T) {
	got := summarizeVPCPeeringConnection(cloud.ResourceSpec{
		Name: "pcx-1", Region: "us-east-1",
		Metadata: map[string]string{"status": "active", "requester_vpc_id": "vpc-a", "accepter_vpc_id": "vpc-b"},
	})
	assert.Contains(t, got, "VPC peering connection pcx-1")
	assert.Contains(t, got, "status=active")
	assert.Contains(t, got, "from=vpc-a")
	assert.Contains(t, got, "to=vpc-b")
}

func TestSummarizeVPCPeeringConnection_EmptyMeta(t *testing.T) {
	assert.Equal(t, "VPC peering connection x", summarizeVPCPeeringConnection(cloud.ResourceSpec{Name: "x"}))
}
