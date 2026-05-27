// SPDX-License-Identifier: Apache-2.0

package gitlab

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("gitlab", "group", summarizeGitLabGroup)
	cicd.Register("gitlab", "project", summarizeGitLabProject)
}

func summarizeGitLabGroup(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab group", spec)
}

func summarizeGitLabProject(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab project", spec)
}
