// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeComputeDisk(t *testing.T) {
	got := summarizeComputeDisk(cloud.ResourceSpec{
		Name: "disk-1",
		Metadata: map[string]string{
			"type": "pd-ssd", "sizeGb": "100", "status": "READY", "zone": "us-central1-a",
		},
	})
	assert.Contains(t, got, "GCE disk disk-1")
	assert.Contains(t, got, "type=pd-ssd")
	assert.Contains(t, got, "size=100GB")
	assert.Contains(t, got, "status=READY")
	assert.Contains(t, got, "in us-central1-a")
}

func TestSummarizeComputeDisk_EmptyMeta(t *testing.T) {
	assert.Equal(t, "GCE disk x", summarizeComputeDisk(cloud.ResourceSpec{Name: "x"}))
}
