// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRole(t *testing.T) {
	assert.Equal(t, "Role r", summarizeRole(cloud.ResourceSpec{Name: "r"}))
}

func TestSummarizeClusterRole(t *testing.T) {
	assert.Equal(t, "ClusterRole cr", summarizeClusterRole(cloud.ResourceSpec{Name: "cr"}))
}
