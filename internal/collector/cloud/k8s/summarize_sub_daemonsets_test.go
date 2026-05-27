// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDaemonSet(t *testing.T) {
	got := summarizeDaemonSet(cloud.ResourceSpec{Name: "ds", Metadata: map[string]string{"namespace": "kube-system"}})
	assert.Equal(t, "DaemonSet ds in kube-system", got)
}

func TestSummarizeDaemonSet_NoNamespace(t *testing.T) {
	assert.Equal(t, "DaemonSet ds", summarizeDaemonSet(cloud.ResourceSpec{Name: "ds"}))
}
