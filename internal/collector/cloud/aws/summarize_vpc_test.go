// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVPC(t *testing.T) {
	got := summarizeVPC(cloud.ResourceSpec{
		Name: "main", Region: "us-east-1",
		Metadata: map[string]string{"cidr_block": "10.0.0.0/16", "is_default": "true", "state": "available"},
	})
	assert.Contains(t, got, "VPC main")
	assert.Contains(t, got, "cidr=10.0.0.0/16")
	assert.Contains(t, got, "default")
	assert.Contains(t, got, "state=available")
}

func TestSummarizeVPC_EmptyMeta(t *testing.T) {
	assert.Equal(t, "VPC x", summarizeVPC(cloud.ResourceSpec{Name: "x"}))
}
