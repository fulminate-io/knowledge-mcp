// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeElastiCacheCluster(t *testing.T) {
	got := summarizeElastiCacheCluster(cloud.ResourceSpec{
		Name: "cache-1", Region: "us-east-1",
		Metadata: map[string]string{"engine": "redis", "engine_version": "7.0", "cache_node_type": "cache.t4g.medium"},
	})
	assert.Contains(t, got, "ElastiCache cluster cache-1")
	assert.Contains(t, got, "engine=redis")
	assert.Contains(t, got, "version=7.0")
	assert.Contains(t, got, "node=cache.t4g.medium")
}

func TestSummarizeElastiCacheCluster_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ElastiCache cluster x", summarizeElastiCacheCluster(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeElastiCacheReplicationGroup(t *testing.T) {
	got := summarizeElastiCacheReplicationGroup(cloud.ResourceSpec{
		Name: "rg-1", Region: "us-east-1",
		Metadata: map[string]string{"description": "primary", "multi_az": "enabled", "status": "available"},
	})
	assert.Contains(t, got, "ElastiCache replication group rg-1")
	assert.Contains(t, got, "(primary)")
	assert.Contains(t, got, "multi_az=enabled")
	assert.Contains(t, got, "status=available")
}

func TestSummarizeElastiCacheReplicationGroup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ElastiCache replication group x", summarizeElastiCacheReplicationGroup(cloud.ResourceSpec{Name: "x"}))
}
