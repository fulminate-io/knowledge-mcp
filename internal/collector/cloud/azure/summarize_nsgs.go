// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/networkSecurityGroups", summarizeNSG)
	cloud.Register("azure:nsg:rule", summarizeNSGRule)
}

func summarizeNSG(spec cloud.ResourceSpec) string {
	return azureGenericSummary("NSG", spec)
}

func summarizeNSGRule(spec cloud.ResourceSpec) string {
	return azureGenericSummary("NSG rule", spec)
}
