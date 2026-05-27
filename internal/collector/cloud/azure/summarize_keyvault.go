// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.KeyVault/vaults", summarizeKeyVault)
}

func summarizeKeyVault(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Key Vault", spec)
}
