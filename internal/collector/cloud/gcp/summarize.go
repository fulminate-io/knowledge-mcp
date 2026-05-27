// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for GCP resource types. One init() per
// source file is the canonical sibling-file convention, but GCP's 40+ types
// follow a uniform gcp:<service>:<kind> shape — a single shared helper for
// metadata-light kinds plus per-source siblings for kinds with rich metadata
// keeps the file count manageable. Per-helper test files mirror this layout.
package gcp

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// gcpGenericSummary returns a generic GCP summary for resources whose
// summarize_*.go sibling intentionally delegates here (no rich metadata to
// surface). Includes ResourceType prefix, name, and zone/region from
// metadata or spec.Region.
func gcpGenericSummary(prefix string, spec cloud.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	if z := spec.Metadata["zone"]; z != "" {
		parts = append(parts, fmt.Sprintf("in %s", z))
	} else if r := spec.Metadata["region"]; r != "" {
		parts = append(parts, fmt.Sprintf("in %s", r))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
