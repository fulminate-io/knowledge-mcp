// SPDX-License-Identifier: Apache-2.0

package gitlab

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("gitlab", "deployment", summarizeGitLabDeployment)
}

func summarizeGitLabDeployment(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab deployment", spec)
}
