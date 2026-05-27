// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectSubscriptions lists subscriptions under a topic and emits each
// subscription as a resource with SUBSCRIBES_TO (subscription → topic),
// CONTAINS (namespace → subscription), and optional DEAD_LETTERS_TO edges.
func (c *serviceBusCollector) collectSubscriptions(
	ctx context.Context,
	client *armservicebus.SubscriptionsClient,
	resourceGroup string,
	ns *armservicebus.SBNamespace,
	topic *armservicebus.SBTopic,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByTopicPager(resourceGroup, *ns.Name, *topic.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, sub := range page.Value {
			if sub.ID == nil || sub.Name == nil {
				continue
			}
			content, err := json.Marshal(sub)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, sbSubscriptionResourceSpec(sub, ns, content))

			// Namespace CONTAINS subscription.
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ns.ID,
				TargetID:     *sub.ID,
				Relationship: kgtypes.EdgeContains,
			})

			// Subscription SUBSCRIBES_TO topic.
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *sub.ID,
				TargetID:     *topic.ID,
				Relationship: kgtypes.EdgeSubscribesTo,
			})

			// Dead letter edges (forwarding or implicit DLQ).
			result.Edges = append(result.Edges, sbSubscriptionDeadLetterEdge(sub)...)
		}
	}
}

// sbSubscriptionResourceSpec builds a ResourceSpec for a Service Bus topic
// subscription, inheriting the namespace location.
func sbSubscriptionResourceSpec(
	sub *armservicebus.SBSubscription,
	ns *armservicebus.SBNamespace,
	content []byte,
) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *sub.ID,
		Name:         *sub.Name,
		ResourceType: "Microsoft.ServiceBus/namespaces/topics/subscriptions",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	return spec
}
