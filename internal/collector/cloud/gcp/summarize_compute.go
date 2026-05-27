// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("gcp:compute:instance", summarizeComputeInstance)
}

func summarizeComputeInstance(spec cloud.ResourceSpec) string {
	parts := []string{"GCE instance", spec.Name}
	if mt := spec.Metadata["machineType"]; mt != "" {
		parts = append(parts, fmt.Sprintf("type=%s", mt))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if z := spec.Metadata["zone"]; z != "" {
		parts = append(parts, fmt.Sprintf("in %s", z))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
