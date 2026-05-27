// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeInternetGateway(t *testing.T) {
	got := summarizeInternetGateway(cloud.ResourceSpec{
		Name: "igw-1", Region: "us-east-1",
		Metadata: map[string]string{"attached_vpc": "vpc-abc"},
	})
	assert.Contains(t, got, "internet gateway igw-1")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeInternetGateway_EmptyMeta(t *testing.T) {
	assert.Equal(t, "internet gateway x", summarizeInternetGateway(cloud.ResourceSpec{Name: "x"}))
}
