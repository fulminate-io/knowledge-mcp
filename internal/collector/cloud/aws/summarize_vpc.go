// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("vpc", summarizeVPC)
}

func summarizeVPC(spec cloud.ResourceSpec) string {
	parts := []string{"VPC", spec.Name}
	if c := spec.Metadata["cidr_block"]; c != "" {
		parts = append(parts, fmt.Sprintf("cidr=%s", c))
	}
	if d := spec.Metadata["is_default"]; d == "true" {
		parts = append(parts, "default")
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
