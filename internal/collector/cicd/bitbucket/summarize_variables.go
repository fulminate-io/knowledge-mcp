// SPDX-License-Identifier: Apache-2.0

package bitbucket

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("bitbucket", "variable", summarizeBBVariable)
}

func summarizeBBVariable(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket variable", spec)
}
