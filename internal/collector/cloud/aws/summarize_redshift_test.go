// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRedshiftCluster(t *testing.T) {
	got := summarizeRedshiftCluster(cloud.ResourceSpec{
		Name: "warehouse", Region: "us-east-1",
		Metadata: map[string]string{"node_type": "ra3.large", "number_of_nodes": "3", "status": "available"},
	})
	assert.Contains(t, got, "Redshift cluster warehouse")
	assert.Contains(t, got, "node=ra3.large")
	assert.Contains(t, got, "nodes=3")
	assert.Contains(t, got, "status=available")
}

func TestSummarizeRedshiftCluster_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Redshift cluster x", summarizeRedshiftCluster(cloud.ResourceSpec{Name: "x"}))
}
