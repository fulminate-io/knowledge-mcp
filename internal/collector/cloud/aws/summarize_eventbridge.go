// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("eventbridge-rule", summarizeEventBridgeRule)
}

func summarizeEventBridgeRule(spec cloud.ResourceSpec) string {
	parts := []string{"EventBridge rule", spec.Name}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if eb := spec.Metadata["event_bus_name"]; eb != "" && eb != "default" {
		parts = append(parts, fmt.Sprintf("bus=%s", eb))
	}
	if se := spec.Metadata["schedule_expression"]; se != "" {
		parts = append(parts, fmt.Sprintf("schedule=%s", se))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
