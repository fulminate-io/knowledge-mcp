// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestK8sNamespacedSummary(t *testing.T) {
	got := k8sNamespacedSummary("Pod", cloud.ResourceSpec{
		Name: "p", Region: "default-ignored",
		Metadata: map[string]string{"namespace": "default"},
	})
	assert.Equal(t, "Pod p in default", got)
}

func TestK8sNamespacedSummary_NoNamespace(t *testing.T) {
	assert.Equal(t, "Pod p", k8sNamespacedSummary("Pod", cloud.ResourceSpec{Name: "p"}))
}

func TestK8sClusterSummary(t *testing.T) {
	assert.Equal(t, "Node n", k8sClusterSummary("Node", cloud.ResourceSpec{Name: "n"}))
}
