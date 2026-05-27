// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("github", "deployment", summarizeGitHubDeployment)
}

func summarizeGitHubDeployment(spec cicd.ResourceSpec) string {
	parts := []string{"GitHub deployment", spec.Name}
	if env := spec.Metadata["environment"]; env != "" {
		parts = append(parts, fmt.Sprintf("env=%s", env))
	}
	if ref := spec.Metadata["ref"]; ref != "" {
		parts = append(parts, fmt.Sprintf("ref=%s", ref))
	}
	if repo := spec.Metadata["repo"]; repo != "" {
		parts = append(parts, fmt.Sprintf("(%s)", repo))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
