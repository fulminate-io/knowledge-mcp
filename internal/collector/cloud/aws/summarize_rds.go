// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("rds-instance", summarizeRDSInstance)
}

func summarizeRDSInstance(spec cloud.ResourceSpec) string {
	parts := []string{"RDS instance", spec.Name}
	if e := spec.Metadata["engine"]; e != "" {
		v := spec.Metadata["engine_version"]
		if v != "" {
			parts = append(parts, fmt.Sprintf("engine=%s/%s", e, v))
		} else {
			parts = append(parts, fmt.Sprintf("engine=%s", e))
		}
	}
	if c := spec.Metadata["instance_class"]; c != "" {
		parts = append(parts, fmt.Sprintf("class=%s", c))
	}
	if maz := spec.Metadata["multi_az"]; maz == "true" {
		parts = append(parts, "multi_az")
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
