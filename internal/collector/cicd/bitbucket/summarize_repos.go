// SPDX-License-Identifier: Apache-2.0

package bitbucket

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("bitbucket", "workspace", summarizeBBWorkspace)
	cicd.Register("bitbucket", "repository", summarizeBBRepository)
}

func summarizeBBWorkspace(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket workspace", spec)
}

func summarizeBBRepository(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket repository", spec)
}
