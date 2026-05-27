// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVPCEndpoint(t *testing.T) {
	got := summarizeVPCEndpoint(cloud.ResourceSpec{
		Name: "ep-1", Region: "us-east-1",
		Metadata: map[string]string{
			"vpc_endpoint_type": "Interface", "service_name": "com.amazonaws.us-east-1.s3",
			"vpc_id": "vpc-abc", "state": "available",
		},
	})
	assert.Contains(t, got, "VPC endpoint ep-1")
	assert.Contains(t, got, "type=Interface")
	assert.Contains(t, got, "service=com.amazonaws.us-east-1.s3")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "state=available")
}

func TestSummarizeVPCEndpoint_EmptyMeta(t *testing.T) {
	assert.Equal(t, "VPC endpoint x", summarizeVPCEndpoint(cloud.ResourceSpec{Name: "x"}))
}
