// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("vpc-peering-connection", summarizeVPCPeeringConnection)
}

func summarizeVPCPeeringConnection(spec cloud.ResourceSpec) string {
	parts := []string{"VPC peering connection", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if r := spec.Metadata["requester_vpc_id"]; r != "" {
		parts = append(parts, fmt.Sprintf("from=%s", r))
	}
	if a := spec.Metadata["accepter_vpc_id"]; a != "" {
		parts = append(parts, fmt.Sprintf("to=%s", a))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
