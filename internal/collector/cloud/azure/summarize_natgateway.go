// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/natGateways", summarizeAzureNATGateway)
}

func summarizeAzureNATGateway(spec cloud.ResourceSpec) string {
	return azureGenericSummary("NAT gateway", spec)
}
