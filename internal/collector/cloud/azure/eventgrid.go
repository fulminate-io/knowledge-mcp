// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type eventGridCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newEventGridCollector(cred azcore.TokenCredential, subID string) *eventGridCollector {
	return &eventGridCollector{cred: cred, subscriptionID: subID}
}

func (c *eventGridCollector) Name() string { return "azure-eventgrid" }

func (c *eventGridCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	topicsClient, err := armeventgrid.NewTopicsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-eventgrid: topics client: %w", err)
	}

	subClient, err := armeventgrid.NewEventSubscriptionsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-eventgrid: subscriptions client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := topicsClient.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-eventgrid: list topics: %w", err)
		}

		for _, topic := range page.Value {
			if topic.ID == nil || topic.Name == nil {
				continue
			}

			content, err := json.Marshal(topic)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, eventGridTopicResourceSpec(topic, content))

			rg := parseResourceGroup(*topic.ID)
			if rg == "" {
				continue
			}
			c.collectEventSubscriptions(ctx, subClient, rg, topic, &result)
		}
	}

	return result, nil
}

// collectEventSubscriptions lists event subscriptions for a topic and emits
// TARGETS edges to each subscription's destination endpoint.
func (c *eventGridCollector) collectEventSubscriptions(
	ctx context.Context,
	client *armeventgrid.EventSubscriptionsClient,
	resourceGroup string,
	topic *armeventgrid.Topic,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByResourcePager(
		resourceGroup, "Microsoft.EventGrid", "topics", *topic.Name, nil,
	)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break // best-effort — continue with other topics
		}

		for _, sub := range page.Value {
			if sub.ID == nil || sub.Name == nil {
				continue
			}

			content, err := json.Marshal(sub)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, eventSubResourceSpec(sub, topic, content))

			if targetID := eventSubDestinationID(sub); targetID != "" {
				result.Edges = append(result.Edges, cloud.EdgeSpec{
					SourceID:     *sub.ID,
					TargetID:     targetID,
					Relationship: kgtypes.EdgeTargets,
				})
			}
		}
	}
}

func eventGridTopicResourceSpec(topic *armeventgrid.Topic, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *topic.ID,
		Name:         *topic.Name,
		ResourceType: "Microsoft.EventGrid/topics",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if topic.Location != nil {
		spec.Region = *topic.Location
	}
	if topic.Properties != nil {
		if topic.Properties.ProvisioningState != nil {
			spec.Metadata["provisioningState"] = string(*topic.Properties.ProvisioningState)
		}
		if topic.Properties.Endpoint != nil {
			spec.Metadata["endpoint"] = *topic.Properties.Endpoint
		}
	}
	return spec
}

func eventSubResourceSpec(
	sub *armeventgrid.EventSubscription,
	topic *armeventgrid.Topic,
	content []byte,
) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *sub.ID,
		Name:         *sub.Name,
		ResourceType: "Microsoft.EventGrid/eventSubscriptions",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if topic.Location != nil {
		spec.Region = *topic.Location
	}
	return spec
}

// eventSubDestinationID extracts the target resource ID or URL from an event
// subscription's destination. Handles Azure Function, Event Hub, Service Bus
// (queue and topic), and webhook destinations.
func eventSubDestinationID(sub *armeventgrid.EventSubscription) string {
	if sub.Properties == nil || sub.Properties.Destination == nil {
		return ""
	}

	switch d := sub.Properties.Destination.(type) {
	case *armeventgrid.AzureFunctionEventSubscriptionDestination:
		if d.Properties != nil && d.Properties.ResourceID != nil {
			return *d.Properties.ResourceID
		}
	case *armeventgrid.EventHubEventSubscriptionDestination:
		if d.Properties != nil && d.Properties.ResourceID != nil {
			return *d.Properties.ResourceID
		}
	case *armeventgrid.ServiceBusQueueEventSubscriptionDestination:
		if d.Properties != nil && d.Properties.ResourceID != nil {
			return *d.Properties.ResourceID
		}
	case *armeventgrid.ServiceBusTopicEventSubscriptionDestination:
		if d.Properties != nil && d.Properties.ResourceID != nil {
			return *d.Properties.ResourceID
		}
	case *armeventgrid.WebHookEventSubscriptionDestination:
		if d.Properties != nil && d.Properties.EndpointURL != nil {
			return *d.Properties.EndpointURL
		}
	}

	return ""
}
