// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeConfigMap(t *testing.T) {
	got := summarizeConfigMap(cloud.ResourceSpec{
		Name: "cm", Metadata: map[string]string{"namespace": "default", "data_key_count": "3"},
	})
	assert.Contains(t, got, "ConfigMap cm")
	assert.Contains(t, got, "keys=3")
	assert.Contains(t, got, "in default")
}

func TestSummarizeConfigMap_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ConfigMap c", summarizeConfigMap(cloud.ResourceSpec{Name: "c"}))
}
