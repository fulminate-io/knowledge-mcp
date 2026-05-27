// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/dnsZones", summarizeDNSZone)
	cloud.Register("Microsoft.Network/dnsZones/recordSets", summarizeDNSRecordSet)
}

func summarizeDNSZone(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure DNS zone", spec)
}

func summarizeDNSRecordSet(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure DNS record set", spec)
}
