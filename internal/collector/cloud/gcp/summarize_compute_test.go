// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeComputeInstance(t *testing.T) {
	got := summarizeComputeInstance(cloud.ResourceSpec{
		Name: "vm-1",
		Metadata: map[string]string{
			"machineType": "n1-standard-1", "status": "RUNNING", "zone": "us-central1-a",
		},
	})
	assert.Contains(t, got, "GCE instance vm-1")
	assert.Contains(t, got, "type=n1-standard-1")
	assert.Contains(t, got, "status=RUNNING")
	assert.Contains(t, got, "in us-central1-a")
}

func TestSummarizeComputeInstance_EmptyMeta(t *testing.T) {
	assert.Equal(t, "GCE instance x", summarizeComputeInstance(cloud.ResourceSpec{Name: "x"}))
}
