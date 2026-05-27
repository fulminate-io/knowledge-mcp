// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("security-group", summarizeSecurityGroup)
}

func summarizeSecurityGroup(spec cloud.ResourceSpec) string {
	parts := []string{"security group", spec.Name}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if d := spec.Metadata["description"]; d != "" {
		parts = append(parts, fmt.Sprintf("(%s)", d))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
