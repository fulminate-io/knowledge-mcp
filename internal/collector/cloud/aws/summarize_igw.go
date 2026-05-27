// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("internet-gateway", summarizeInternetGateway)
}

func summarizeInternetGateway(spec cloud.ResourceSpec) string {
	parts := []string{"internet gateway", spec.Name}
	if v := spec.Metadata["attached_vpc"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
