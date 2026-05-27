// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for Azure resource types. ARM
// resource type strings (Microsoft.<service>/...) feed into per-source
// sibling files; a small generic helper covers metadata-light kinds while
// kinds with rich metadata get bespoke formatters.
package azure

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// azureGenericSummary returns a generic Azure summary for resources whose
// summarize_*.go sibling intentionally delegates here. Includes the prefix,
// name, and location (from spec.Region or metadata).
func azureGenericSummary(prefix string, spec cloud.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	if loc := spec.Metadata["location"]; loc != "" {
		parts = append(parts, fmt.Sprintf("in %s", loc))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
