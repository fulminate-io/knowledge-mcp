// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("nat-gateway", summarizeNATGateway)
}

func summarizeNATGateway(spec cloud.ResourceSpec) string {
	parts := []string{"NAT gateway", spec.Name}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if c := spec.Metadata["connectivity_type"]; c != "" {
		parts = append(parts, fmt.Sprintf("connectivity=%s", c))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
