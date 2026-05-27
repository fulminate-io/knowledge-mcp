// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/privateDnsZones", summarizePrivateDNSZone)
}

func summarizePrivateDNSZone(spec cloud.ResourceSpec) string {
	return azureGenericSummary("private DNS zone", spec)
}
