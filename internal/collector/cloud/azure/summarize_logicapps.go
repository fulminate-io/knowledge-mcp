// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Logic/workflows", summarizeLogicAppWorkflow)
}

func summarizeLogicAppWorkflow(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Logic App workflow", spec)
}
