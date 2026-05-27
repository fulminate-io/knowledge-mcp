// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeService(t *testing.T) {
	got := summarizeService(cloud.ResourceSpec{
		Name: "frontend", Metadata: map[string]string{"namespace": "default", "type": "ClusterIP"},
	})
	assert.Contains(t, got, "Service frontend")
	assert.Contains(t, got, "type=ClusterIP")
	assert.Contains(t, got, "in default")
}

func TestSummarizeService_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Service s", summarizeService(cloud.ResourceSpec{Name: "s"}))
}
