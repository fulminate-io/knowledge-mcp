// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEBSVolume(t *testing.T) {
	got := summarizeEBSVolume(cloud.ResourceSpec{
		Name: "vol-1", Region: "us-east-1",
		Metadata: map[string]string{
			"volume_type": "gp3", "size_gib": "100", "state": "in-use",
			"encrypted": "true", "availability_zone": "us-east-1a",
		},
	})
	assert.Contains(t, got, "EBS volume vol-1")
	assert.Contains(t, got, "type=gp3")
	assert.Contains(t, got, "size=100GiB")
	assert.Contains(t, got, "state=in-use")
	assert.Contains(t, got, "encrypted")
	assert.Contains(t, got, "in us-east-1a")
}

func TestSummarizeEBSVolume_EmptyMeta(t *testing.T) {
	assert.Equal(t, "EBS volume x", summarizeEBSVolume(cloud.ResourceSpec{Name: "x"}))
}
