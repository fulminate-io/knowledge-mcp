// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("network-acl", summarizeNetworkACL)
}

func summarizeNetworkACL(spec cloud.ResourceSpec) string {
	parts := []string{"network ACL", spec.Name}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if d := spec.Metadata["is_default"]; d == "true" {
		parts = append(parts, "default")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
