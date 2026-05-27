// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for GitHub CI/CD resource types.
// One init() per source file is the canonical sibling-file convention; the
// helper below provides a generic shape for kinds with no rich metadata.
package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

// ghGenericSummary returns a "<prefix> <name>" summary, optionally
// suffixed with "(org/repo)" when both metadata keys are present.
func ghGenericSummary(prefix string, spec cicd.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	org := spec.Metadata["org"]
	repo := spec.Metadata["repo"]
	if org != "" && repo != "" {
		parts = append(parts, fmt.Sprintf("(%s/%s)", org, repo))
	} else if org != "" {
		parts = append(parts, fmt.Sprintf("(%s)", org))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
