// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("transit-gateway", summarizeTransitGateway)
	cloud.Register("transit-gateway-attachment", summarizeTransitGatewayAttachment)
}

func summarizeTransitGateway(spec cloud.ResourceSpec) string {
	parts := []string{"transit gateway", spec.Name}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if a := spec.Metadata["amazon_side_asn"]; a != "" {
		parts = append(parts, fmt.Sprintf("asn=%s", a))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeTransitGatewayAttachment(spec cloud.ResourceSpec) string {
	parts := []string{"transit gateway attachment", spec.Name}
	if t := spec.Metadata["resource_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if r := spec.Metadata["resource_id"]; r != "" {
		parts = append(parts, fmt.Sprintf("for=%s", r))
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
