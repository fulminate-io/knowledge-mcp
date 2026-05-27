// SPDX-License-Identifier: Apache-2.0

package bitbucket

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("bitbucket", "runner", summarizeBBRunner)
	cicd.Register("bitbucket", "label", summarizeBBLabel)
}

func summarizeBBRunner(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket runner", spec)
}

func summarizeBBLabel(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket runner label", spec)
}
