// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDeployment(t *testing.T) {
	got := summarizeDeployment(cloud.ResourceSpec{
		Name: "web", Metadata: map[string]string{"namespace": "prod", "replicas": "3", "strategy": "RollingUpdate"},
	})
	assert.Contains(t, got, "Deployment web")
	assert.Contains(t, got, "replicas=3")
	assert.Contains(t, got, "strategy=RollingUpdate")
	assert.Contains(t, got, "in prod")
}

func TestSummarizeDeployment_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Deployment d", summarizeDeployment(cloud.ResourceSpec{Name: "d"}))
}
