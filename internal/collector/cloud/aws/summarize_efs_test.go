// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEFSFilesystem(t *testing.T) {
	got := summarizeEFSFilesystem(cloud.ResourceSpec{
		Name: "fs-1", Region: "us-east-1",
		Metadata: map[string]string{
			"encrypted": "true", "performance_mode": "generalPurpose",
			"throughput_mode": "bursting", "life_cycle_state": "available",
		},
	})
	assert.Contains(t, got, "EFS filesystem fs-1")
	assert.Contains(t, got, "encrypted")
	assert.Contains(t, got, "perf=generalPurpose")
	assert.Contains(t, got, "throughput=bursting")
	assert.Contains(t, got, "state=available")
}

func TestSummarizeEFSFilesystem_EmptyMeta(t *testing.T) {
	assert.Equal(t, "EFS filesystem x", summarizeEFSFilesystem(cloud.ResourceSpec{Name: "x"}))
}
