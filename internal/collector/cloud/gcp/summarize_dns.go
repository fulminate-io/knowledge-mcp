// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:dns:managedZone", summarizeDNSManagedZone)
	cloud.Register("gcp:dns:recordSet", summarizeDNSRecordSet)
}

func summarizeDNSManagedZone(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("DNS managed zone", spec)
}

func summarizeDNSRecordSet(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("DNS record set", spec)
}
