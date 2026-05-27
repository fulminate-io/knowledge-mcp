// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ServiceBus/namespaces/topics/subscriptions", summarizeServiceBusSubscription)
}

func summarizeServiceBusSubscription(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Service Bus subscription", spec)
}
