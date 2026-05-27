// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ServiceBus/namespaces", summarizeServiceBusNamespace)
	cloud.Register("Microsoft.ServiceBus/namespaces/queues", summarizeServiceBusQueue)
	cloud.Register("Microsoft.ServiceBus/namespaces/topics", summarizeServiceBusTopic)
}

func summarizeServiceBusNamespace(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Service Bus namespace", spec)
}

func summarizeServiceBusQueue(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Service Bus queue", spec)
}

func summarizeServiceBusTopic(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Service Bus topic", spec)
}
