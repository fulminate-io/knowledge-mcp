// SPDX-License-Identifier: Apache-2.0

package github

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("github", "runner", summarizeGitHubRunner)
}

func summarizeGitHubRunner(spec cicd.ResourceSpec) string {
	return ghGenericSummary("GitHub runner", spec)
}
