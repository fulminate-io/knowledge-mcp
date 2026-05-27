// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEC2Instance(t *testing.T) {
	got := summarizeEC2Instance(cloud.ResourceSpec{
		Name: "web-1", Region: "us-east-1",
		Metadata: map[string]string{
			"instance_type": "m5.large", "state": "running",
			"vpc_id": "vpc-abc", "availability_zone": "us-east-1a",
		},
	})
	assert.Contains(t, got, "EC2 instance web-1")
	assert.Contains(t, got, "type=m5.large")
	assert.Contains(t, got, "state=running")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "in us-east-1a")
}

func TestSummarizeEC2Instance_EmptyMeta(t *testing.T) {
	assert.Equal(t, "EC2 instance x", summarizeEC2Instance(cloud.ResourceSpec{Name: "x"}))
}
