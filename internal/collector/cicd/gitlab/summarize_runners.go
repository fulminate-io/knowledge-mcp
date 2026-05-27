// SPDX-License-Identifier: Apache-2.0

package gitlab

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("gitlab", "runner", summarizeGitLabRunner)
	cicd.Register("gitlab", "runner-tag", summarizeGitLabRunnerTag)
}

func summarizeGitLabRunner(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab runner", spec)
}

func summarizeGitLabRunnerTag(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab runner tag", spec)
}
