// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ses-receipt-rule", summarizeSESReceiptRule)
}

func summarizeSESReceiptRule(spec cloud.ResourceSpec) string {
	parts := []string{"SES receipt rule", spec.Name}
	if e := spec.Metadata["enabled"]; e == "true" {
		parts = append(parts, "enabled")
	}
	if s := spec.Metadata["scan_enabled"]; s == "true" {
		parts = append(parts, "spam-scan")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
