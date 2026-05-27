// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("efs-filesystem", summarizeEFSFilesystem)
}

func summarizeEFSFilesystem(spec cloud.ResourceSpec) string {
	parts := []string{"EFS filesystem", spec.Name}
	if e := spec.Metadata["encrypted"]; e == "true" {
		parts = append(parts, "encrypted")
	}
	if pm := spec.Metadata["performance_mode"]; pm != "" {
		parts = append(parts, fmt.Sprintf("perf=%s", pm))
	}
	if tm := spec.Metadata["throughput_mode"]; tm != "" {
		parts = append(parts, fmt.Sprintf("throughput=%s", tm))
	}
	if ls := spec.Metadata["life_cycle_state"]; ls != "" {
		parts = append(parts, fmt.Sprintf("state=%s", ls))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
