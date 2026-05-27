// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Synapse/workspaces/sqlPools", summarizeSynapseSQLPool)
	cloud.Register("Microsoft.Synapse/workspaces/bigDataPools", summarizeSynapseSparkPool)
}

func summarizeSynapseSQLPool(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Synapse SQL pool", spec)
}

func summarizeSynapseSparkPool(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Synapse Spark pool", spec)
}
