// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for Bitbucket CI/CD resource types.
package bitbucket

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

// bbGenericSummary returns "<prefix> <name>" with optional "(workspace/repo)"
// or "(workspace)" suffix from metadata.
func bbGenericSummary(prefix string, spec cicd.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	ws := spec.Metadata["workspace"]
	repo := spec.Metadata["repo"]
	if ws != "" && repo != "" {
		parts = append(parts, fmt.Sprintf("(%s/%s)", ws, repo))
	} else if ws != "" {
		parts = append(parts, fmt.Sprintf("(%s)", ws))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
