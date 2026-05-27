// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.EventHub/namespaces", summarizeEventHubNamespace)
	cloud.Register("Microsoft.EventHub/namespaces/eventhubs", summarizeEventHub)
	cloud.Register("Microsoft.EventHub/namespaces/eventhubs/consumergroups", summarizeEventHubConsumerGroup)
}

func summarizeEventHubNamespace(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Event Hub namespace", spec)
}

func summarizeEventHub(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Event Hub", spec)
}

func summarizeEventHubConsumerGroup(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Event Hub consumer group", spec)
}
