// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/networkWatchers/flowLogs", summarizeAzureFlowLog)
}

func summarizeAzureFlowLog(spec cloud.ResourceSpec) string {
	return azureGenericSummary("network flow log", spec)
}
