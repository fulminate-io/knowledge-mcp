// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ManagedIdentity/userAssignedIdentities", summarizeUserAssignedIdentity)
}

func summarizeUserAssignedIdentity(spec cloud.ResourceSpec) string {
	return azureGenericSummary("user-assigned managed identity", spec)
}
