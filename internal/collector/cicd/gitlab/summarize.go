// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for GitLab CI/CD resource types.
package gitlab

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

// glGenericSummary returns "<prefix> <name>" with optional "(project)" or
// "(group)" suffix from metadata.
func glGenericSummary(prefix string, spec cicd.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	if p := spec.Metadata["project"]; p != "" {
		parts = append(parts, fmt.Sprintf("(%s)", p))
	} else if g := spec.Metadata["group"]; g != "" {
		parts = append(parts, fmt.Sprintf("(%s)", g))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
