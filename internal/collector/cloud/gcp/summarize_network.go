// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:compute:network", summarizeComputeNetwork)
	cloud.Register("gcp:compute:subnetwork", summarizeComputeSubnetwork)
	cloud.Register("gcp:compute:firewall", summarizeComputeFirewall)
}

func summarizeComputeNetwork(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("VPC network", spec)
}

func summarizeComputeSubnetwork(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("subnetwork", spec)
}

func summarizeComputeFirewall(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("firewall rule", spec)
}
