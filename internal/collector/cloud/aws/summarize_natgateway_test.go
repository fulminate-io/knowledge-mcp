// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeNATGateway(t *testing.T) {
	got := summarizeNATGateway(cloud.ResourceSpec{
		Name: "nat-1", Region: "us-east-1",
		Metadata: map[string]string{"state": "available", "connectivity_type": "public", "vpc_id": "vpc-abc"},
	})
	assert.Contains(t, got, "NAT gateway nat-1")
	assert.Contains(t, got, "state=available")
	assert.Contains(t, got, "connectivity=public")
	assert.Contains(t, got, "vpc=vpc-abc")
}

func TestSummarizeNATGateway_EmptyMeta(t *testing.T) {
	assert.Equal(t, "NAT gateway x", summarizeNATGateway(cloud.ResourceSpec{Name: "x"}))
}
