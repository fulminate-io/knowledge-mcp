// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSubnet(t *testing.T) {
	got := summarizeSubnet(cloud.ResourceSpec{
		Name: "private-1", Region: "us-east-1",
		Metadata: map[string]string{"cidr_block": "10.0.1.0/24", "vpc_id": "vpc-abc", "availability_zone": "us-east-1a"},
	})
	assert.Contains(t, got, "subnet private-1")
	assert.Contains(t, got, "cidr=10.0.1.0/24")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "in us-east-1a")
}

func TestSummarizeSubnet_EmptyMeta(t *testing.T) {
	assert.Equal(t, "subnet x", summarizeSubnet(cloud.ResourceSpec{Name: "x"}))
}
