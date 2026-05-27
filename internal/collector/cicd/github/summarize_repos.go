// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("github", "organization", summarizeGitHubOrganization)
	cicd.Register("github", "repository", summarizeGitHubRepository)
}

func summarizeGitHubOrganization(spec cicd.ResourceSpec) string {
	return strings.TrimSpace(fmt.Sprintf("GitHub organization %s", spec.Name))
}

func summarizeGitHubRepository(spec cicd.ResourceSpec) string {
	parts := []string{"GitHub repository", spec.Name}
	if v := spec.Metadata["visibility"]; v != "" {
		parts = append(parts, fmt.Sprintf("visibility=%s", v))
	}
	if b := spec.Metadata["default_branch"]; b != "" {
		parts = append(parts, fmt.Sprintf("default=%s", b))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
