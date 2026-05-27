// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeHPA(t *testing.T) {
	got := summarizeHPA(cloud.ResourceSpec{
		Name: "h", Metadata: map[string]string{"namespace": "prod", "min_replicas": "2", "max_replicas": "10", "current_replicas": "5"},
	})
	assert.Contains(t, got, "HPA h")
	assert.Contains(t, got, "range=2..10")
	assert.Contains(t, got, "current=5")
	assert.Contains(t, got, "in prod")
}

func TestSummarizeHPA_EmptyMeta(t *testing.T) {
	assert.Equal(t, "HPA x", summarizeHPA(cloud.ResourceSpec{Name: "x"}))
}
