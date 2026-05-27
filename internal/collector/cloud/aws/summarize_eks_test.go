// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEKSCluster(t *testing.T) {
	got := summarizeEKSCluster(cloud.ResourceSpec{
		Name: "prod", Region: "us-east-1",
		Metadata: map[string]string{"version": "1.28", "status": "ACTIVE"},
	})
	assert.Contains(t, got, "EKS cluster prod")
	assert.Contains(t, got, "k8s=1.28")
	assert.Contains(t, got, "status=ACTIVE")
}

func TestSummarizeEKSCluster_EmptyMeta(t *testing.T) {
	assert.Equal(t, "EKS cluster x", summarizeEKSCluster(cloud.ResourceSpec{Name: "x"}))
}
