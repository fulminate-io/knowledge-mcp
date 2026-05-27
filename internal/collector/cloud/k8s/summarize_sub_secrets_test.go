// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSecret(t *testing.T) {
	got := summarizeSecret(cloud.ResourceSpec{
		Name: "tls-cert", Metadata: map[string]string{"namespace": "prod", "type": "kubernetes.io/tls"},
	})
	assert.Contains(t, got, "Secret tls-cert")
	assert.Contains(t, got, "type=kubernetes.io/tls")
	assert.Contains(t, got, "in prod")
}

func TestSummarizeSecret_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Secret s", summarizeSecret(cloud.ResourceSpec{Name: "s"}))
}
