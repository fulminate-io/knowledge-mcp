// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("github", "workflow_run", summarizeGitHubWorkflowRun)
}

func summarizeGitHubWorkflowRun(spec cicd.ResourceSpec) string {
	parts := []string{"GitHub workflow run", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if c := spec.Metadata["conclusion"]; c != "" {
		parts = append(parts, fmt.Sprintf("conclusion=%s", c))
	}
	if e := spec.Metadata["event"]; e != "" {
		parts = append(parts, fmt.Sprintf("event=%s", e))
	}
	if r := spec.Metadata["repo"]; r != "" {
		parts = append(parts, fmt.Sprintf("(%s)", r))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
