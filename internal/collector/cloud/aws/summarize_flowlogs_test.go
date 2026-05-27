// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeFlowLog(t *testing.T) {
	got := summarizeFlowLog(cloud.ResourceSpec{
		Name: "fl-1", Region: "us-east-1",
		Metadata: map[string]string{"traffic_type": "ALL", "resource_id": "vpc-abc", "log_destination_type": "s3"},
	})
	assert.Contains(t, got, "VPC flow log fl-1")
	assert.Contains(t, got, "traffic=ALL")
	assert.Contains(t, got, "for=vpc-abc")
	assert.Contains(t, got, "dest=s3")
}

func TestSummarizeFlowLog_EmptyMeta(t *testing.T) {
	assert.Equal(t, "VPC flow log x", summarizeFlowLog(cloud.ResourceSpec{Name: "x"}))
}
