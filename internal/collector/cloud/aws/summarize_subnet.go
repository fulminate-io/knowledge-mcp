// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("subnet", summarizeSubnet)
}

func summarizeSubnet(spec cloud.ResourceSpec) string {
	parts := []string{"subnet", spec.Name}
	if c := spec.Metadata["cidr_block"]; c != "" {
		parts = append(parts, fmt.Sprintf("cidr=%s", c))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if az := spec.Metadata["availability_zone"]; az != "" {
		parts = append(parts, fmt.Sprintf("in %s", az))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
