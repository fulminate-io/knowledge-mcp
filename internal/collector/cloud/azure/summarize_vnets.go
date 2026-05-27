// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/virtualNetworks", summarizeVNet)
	cloud.Register("Microsoft.Network/virtualNetworks/subnets", summarizeVNetSubnet)
}

func summarizeVNet(spec cloud.ResourceSpec) string {
	return azureGenericSummary("VNet", spec)
}

func summarizeVNetSubnet(spec cloud.ResourceSpec) string {
	return azureGenericSummary("VNet subnet", spec)
}
