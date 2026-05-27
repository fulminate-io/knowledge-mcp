// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:workflows:workflow", summarizeWorkflowsWorkflow)
}

func summarizeWorkflowsWorkflow(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Workflows workflow", spec)
}
