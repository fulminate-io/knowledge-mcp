// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.EventGrid/topics", summarizeEventGridTopic)
	cloud.Register("Microsoft.EventGrid/eventSubscriptions", summarizeEventGridSubscription)
}

func summarizeEventGridTopic(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Event Grid topic", spec)
}

func summarizeEventGridSubscription(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Event Grid subscription", spec)
}
