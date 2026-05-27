// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizePod(t *testing.T) {
	got := summarizePod(cloud.ResourceSpec{
		Name: "web-1",
		Metadata: map[string]string{
			"namespace": "default", "phase": "Running", "node_name": "node-1", "restarts": "2",
		},
	})
	assert.Contains(t, got, "Pod web-1")
	assert.Contains(t, got, "phase=Running")
	assert.Contains(t, got, "node=node-1")
	assert.Contains(t, got, "restarts=2")
	assert.Contains(t, got, "in default")
}

func TestSummarizePod_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Pod p", summarizePod(cloud.ResourceSpec{Name: "p"}))
}
