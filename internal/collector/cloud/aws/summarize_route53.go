// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("route53-hostedzone", summarizeRoute53HostedZone)
}

func summarizeRoute53HostedZone(spec cloud.ResourceSpec) string {
	parts := []string{"Route53 hosted zone", spec.Name}
	if p := spec.Metadata["private_zone"]; p == "true" {
		parts = append(parts, "private")
	}
	if c := spec.Metadata["resource_record_set_count"]; c != "" {
		parts = append(parts, fmt.Sprintf("records=%s", c))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
