// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid/v2"
	"github.com/stretchr/testify/assert"
)

func TestEventGridCollector_Name(t *testing.T) {
	c := &eventGridCollector{}
	assert.Equal(t, "azure-eventgrid", c.Name())
}

func TestEventSubDestinationID(t *testing.T) {
	funcID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/sites/myFunc"
	hubID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/ns/eventhubs/hub1"
	sbQueueID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/queues/q1"
	sbTopicID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/topics/t1"
	webhookURL := "https://example.com/webhook"

	tests := []struct {
		name string
		sub  *armeventgrid.EventSubscription
		want string
	}{
		{
			name: "nil properties",
			sub:  &armeventgrid.EventSubscription{},
			want: "",
		},
		{
			name: "nil destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{},
			},
			want: "",
		},
		{
			name: "azure function destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.AzureFunctionEventSubscriptionDestination{
						Properties: &armeventgrid.AzureFunctionEventSubscriptionDestinationProperties{
							ResourceID: &funcID,
						},
					},
				},
			},
			want: funcID,
		},
		{
			name: "event hub destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.EventHubEventSubscriptionDestination{
						Properties: &armeventgrid.EventHubEventSubscriptionDestinationProperties{
							ResourceID: &hubID,
						},
					},
				},
			},
			want: hubID,
		},
		{
			name: "service bus queue destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.ServiceBusQueueEventSubscriptionDestination{
						Properties: &armeventgrid.ServiceBusQueueEventSubscriptionDestinationProperties{
							ResourceID: &sbQueueID,
						},
					},
				},
			},
			want: sbQueueID,
		},
		{
			name: "service bus topic destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.ServiceBusTopicEventSubscriptionDestination{
						Properties: &armeventgrid.ServiceBusTopicEventSubscriptionDestinationProperties{
							ResourceID: &sbTopicID,
						},
					},
				},
			},
			want: sbTopicID,
		},
		{
			name: "webhook destination",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.WebHookEventSubscriptionDestination{
						Properties: &armeventgrid.WebHookEventSubscriptionDestinationProperties{
							EndpointURL: &webhookURL,
						},
					},
				},
			},
			want: webhookURL,
		},
		{
			name: "azure function with nil properties",
			sub: &armeventgrid.EventSubscription{
				Properties: &armeventgrid.EventSubscriptionProperties{
					Destination: &armeventgrid.AzureFunctionEventSubscriptionDestination{},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventSubDestinationID(tt.sub)
			assert.Equal(t, tt.want, got)
		})
	}
}
