// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Sql/servers", summarizeSQLServer)
	cloud.Register("Microsoft.Sql/servers/databases", summarizeSQLDatabase)
}

func summarizeSQLServer(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure SQL server", spec)
}

func summarizeSQLDatabase(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure SQL database", spec)
}
