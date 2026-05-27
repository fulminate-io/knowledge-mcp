// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.DocumentDB/databaseAccounts", summarizeCosmosDB)
}

func summarizeCosmosDB(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Cosmos DB account", spec)
}
