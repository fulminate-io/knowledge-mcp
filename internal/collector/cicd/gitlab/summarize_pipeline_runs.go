// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("gitlab", "pipeline-run", summarizeGitLabPipelineRun)
	cicd.Register("gitlab", "job", summarizeGitLabJob)
}

func summarizeGitLabPipelineRun(spec cicd.ResourceSpec) string {
	parts := []string{"GitLab pipeline run", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if r := spec.Metadata["ref"]; r != "" {
		parts = append(parts, fmt.Sprintf("ref=%s", r))
	}
	if p := spec.Metadata["project"]; p != "" {
		parts = append(parts, fmt.Sprintf("(%s)", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeGitLabJob(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab job", spec)
}
