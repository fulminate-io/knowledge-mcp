// SPDX-License-Identifier: Apache-2.0

package github

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("github", "environment", summarizeGitHubEnvironment)
}

func summarizeGitHubEnvironment(spec cicd.ResourceSpec) string {
	return ghGenericSummary("GitHub environment", spec)
}
