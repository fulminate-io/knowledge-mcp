// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Synapse/workspaces", summarizeSynapseWorkspace)
}

func summarizeSynapseWorkspace(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Synapse workspace", spec)
}
