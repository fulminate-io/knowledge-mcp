// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ecs-cluster", summarizeECSCluster)
	cloud.Register("ecs-service", summarizeECSService)
}

func summarizeECSCluster(spec cloud.ResourceSpec) string {
	parts := []string{"ECS cluster", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeECSService(spec cloud.ResourceSpec) string {
	parts := []string{"ECS service", spec.Name}
	if lt := spec.Metadata["launch_type"]; lt != "" {
		parts = append(parts, fmt.Sprintf("launch=%s", lt))
	}
	if dc := spec.Metadata["desired_count"]; dc != "" {
		parts = append(parts, fmt.Sprintf("desired=%s", dc))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
