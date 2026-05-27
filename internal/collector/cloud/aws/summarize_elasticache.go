// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("elasticache-cluster", summarizeElastiCacheCluster)
	cloud.Register("elasticache-replication-group", summarizeElastiCacheReplicationGroup)
}

func summarizeElastiCacheCluster(spec cloud.ResourceSpec) string {
	parts := []string{"ElastiCache cluster", spec.Name}
	if e := spec.Metadata["engine"]; e != "" {
		parts = append(parts, fmt.Sprintf("engine=%s", e))
	}
	if v := spec.Metadata["engine_version"]; v != "" {
		parts = append(parts, fmt.Sprintf("version=%s", v))
	}
	if t := spec.Metadata["cache_node_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("node=%s", t))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeElastiCacheReplicationGroup(spec cloud.ResourceSpec) string {
	parts := []string{"ElastiCache replication group", spec.Name}
	if d := spec.Metadata["description"]; d != "" {
		parts = append(parts, fmt.Sprintf("(%s)", d))
	}
	if maz := spec.Metadata["multi_az"]; maz != "" {
		parts = append(parts, fmt.Sprintf("multi_az=%s", maz))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
