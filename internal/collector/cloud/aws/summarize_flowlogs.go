// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("flow-log", summarizeFlowLog)
}

func summarizeFlowLog(spec cloud.ResourceSpec) string {
	parts := []string{"VPC flow log", spec.Name}
	if t := spec.Metadata["traffic_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("traffic=%s", t))
	}
	if r := spec.Metadata["resource_id"]; r != "" {
		parts = append(parts, fmt.Sprintf("for=%s", r))
	}
	if d := spec.Metadata["log_destination_type"]; d != "" {
		parts = append(parts, fmt.Sprintf("dest=%s", d))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
