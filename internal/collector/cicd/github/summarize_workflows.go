// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("github", "workflow", summarizeGitHubWorkflow)
}

func summarizeGitHubWorkflow(spec cicd.ResourceSpec) string {
	parts := []string{"GitHub workflow", spec.Name}
	if p := spec.Metadata["path"]; p != "" {
		parts = append(parts, fmt.Sprintf("path=%s", p))
	}
	if r := spec.Metadata["repo"]; r != "" {
		parts = append(parts, fmt.Sprintf("(%s)", r))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
