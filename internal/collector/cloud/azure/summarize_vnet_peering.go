// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/virtualNetworks/virtualNetworkPeerings", summarizeVNetPeering)
}

func summarizeVNetPeering(spec cloud.ResourceSpec) string {
	return azureGenericSummary("VNet peering", spec)
}
