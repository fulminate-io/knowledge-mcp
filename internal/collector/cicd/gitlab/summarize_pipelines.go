// SPDX-License-Identifier: Apache-2.0

package gitlab

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("gitlab", "pipeline", summarizeGitLabPipeline)
}

func summarizeGitLabPipeline(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab pipeline", spec)
}
