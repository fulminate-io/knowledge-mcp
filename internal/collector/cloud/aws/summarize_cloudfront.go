// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("cloudfront-distribution", summarizeCloudFrontDistribution)
}

func summarizeCloudFrontDistribution(spec cloud.ResourceSpec) string {
	parts := []string{"CloudFront distribution", spec.Name}
	if e := spec.Metadata["enabled"]; e != "" {
		parts = append(parts, fmt.Sprintf("enabled=%s", e))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if pc := spec.Metadata["price_class"]; pc != "" {
		parts = append(parts, fmt.Sprintf("price=%s", pc))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
