// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("vpc-endpoint", summarizeVPCEndpoint)
}

func summarizeVPCEndpoint(spec cloud.ResourceSpec) string {
	parts := []string{"VPC endpoint", spec.Name}
	if t := spec.Metadata["vpc_endpoint_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if svc := spec.Metadata["service_name"]; svc != "" {
		parts = append(parts, fmt.Sprintf("service=%s", svc))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
