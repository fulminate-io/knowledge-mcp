// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/loadBalancers", summarizeLoadBalancer)
}

func summarizeLoadBalancer(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure load balancer", spec)
}
