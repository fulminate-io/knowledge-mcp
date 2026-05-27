// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("github", "secret", summarizeGitHubSecret)
}

func summarizeGitHubSecret(spec cicd.ResourceSpec) string {
	parts := []string{"GitHub secret", spec.Name}
	if scope := spec.Metadata["scope"]; scope != "" {
		parts = append(parts, fmt.Sprintf("scope=%s", scope))
	}
	if org := spec.Metadata["org"]; org != "" {
		parts = append(parts, fmt.Sprintf("(%s)", org))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
