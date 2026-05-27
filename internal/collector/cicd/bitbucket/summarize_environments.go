// SPDX-License-Identifier: Apache-2.0

package bitbucket

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"

func init() {
	cicd.Register("bitbucket", "environment", summarizeBBEnvironment)
	cicd.Register("bitbucket", "approval_gate", summarizeBBApprovalGate)
}

func summarizeBBEnvironment(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket environment", spec)
}

func summarizeBBApprovalGate(spec cicd.ResourceSpec) string {
	return bbGenericSummary("Bitbucket approval gate", spec)
}
