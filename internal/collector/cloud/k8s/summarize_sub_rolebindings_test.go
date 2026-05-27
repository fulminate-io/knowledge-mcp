// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRoleBinding(t *testing.T) {
	assert.Equal(t, "RoleBinding rb", summarizeRoleBinding(cloud.ResourceSpec{Name: "rb"}))
}

func TestSummarizeClusterRoleBinding(t *testing.T) {
	assert.Equal(t, "ClusterRoleBinding crb", summarizeClusterRoleBinding(cloud.ResourceSpec{Name: "crb"}))
}
