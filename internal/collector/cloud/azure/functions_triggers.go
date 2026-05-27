// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectFunctionTriggers lists individual functions in a function app and
// parses their config to extract trigger bindings. For each trigger, it emits
// a TRIGGERS edge from the function app to a sentinel proxy node for the
// inferred target resource. Sentinel proxies allow the edge to resolve until
// a future postpopulate pass joins them against real Service Bus / Event Hub
// / Storage resource IDs.
func (c *functionsCollector) collectFunctionTriggers(
	ctx context.Context,
	client *armappservice.WebAppsClient,
	site *armappservice.Site,
	seenProxies map[string]bool,
	result *cloud.SubCollectorResult,
) {
	rg := parseResourceGroup(*site.ID)
	if rg == "" {
		return
	}

	pager := client.NewListFunctionsPager(rg, *site.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, fn := range page.Value {
			if fn.Properties == nil || fn.Properties.Config == nil {
				continue
			}
			edges, proxies := parseTriggerBindings(*site.ID, c.subscriptionID, fn.Properties.Config, seenProxies)
			result.Edges = append(result.Edges, edges...)
			result.Resources = append(result.Resources, proxies...)
		}
	}
}

// parseTriggerBindings extracts trigger binding edges from a function's Config
// JSON and returns the edges plus any new sentinel proxy ResourceSpecs needed
// to back the edge targets. The Config field is `any` in the SDK; we
// marshal/unmarshal to parse the bindings array. seenProxies is shared across
// the collector run so a queue/topic/hub referenced by multiple functions
// emits at most one proxy node.
func parseTriggerBindings(
	appID, subscriptionID string,
	config any,
	seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, nil
	}
	var cfg functionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, nil
	}

	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec
	for _, b := range cfg.Bindings {
		if !strings.HasSuffix(strings.ToLower(b.Type), "trigger") {
			continue
		}
		sentinelID, resourceType, name := triggerSentinel(b)
		if sentinelID == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     appID,
			TargetID:     sentinelID,
			Relationship: kgtypes.EdgeTriggers,
			Metadata:     map[string]string{"triggerType": b.Type},
		})
		if seenProxies != nil && !seenProxies[sentinelID] {
			seenProxies[sentinelID] = true
			proxies = append(proxies, cloud.ResourceSpec{
				ID:           sentinelID,
				Name:         name,
				ResourceType: resourceType,
				Metadata: map[string]string{
					"collected":        "false",
					"collected_reason": "function trigger reference resolved without resource group or namespace",
					"discovered_via":   "function app trigger binding",
					"subscription_id":  subscriptionID,
				},
			})
		}
	}
	return edges, proxies
}

// triggerSentinel returns the (sentinel ID, resource type, display name) for a
// trigger binding's target. The sentinel ID is a stable, namespaced synthetic
// identifier that a postpopulate resolver can later promote to the real ARM
// resource ID once the matching subcollector lands. Connection-string
// settings are opaque at this layer, so we key on the queue/topic/hub/path
// name only.
func triggerSentinel(b functionBinding) (sentinelID, resourceType, name string) {
	lower := strings.ToLower(b.Type)
	switch {
	case strings.Contains(lower, "servicebus"):
		if b.QueueName != "" {
			return fmt.Sprintf("azure:servicebus:queue:%s", b.QueueName),
				"azure:servicebus:queue",
				b.QueueName
		}
		if b.TopicName != "" {
			return fmt.Sprintf("azure:servicebus:topic:%s", b.TopicName),
				"azure:servicebus:topic",
				b.TopicName
		}
	case strings.Contains(lower, "eventhub"):
		if b.EventHubName != "" {
			return fmt.Sprintf("azure:eventhub:hub:%s", b.EventHubName),
				"azure:eventhub:hub",
				b.EventHubName
		}
	case strings.Contains(lower, "queue") || strings.Contains(lower, "blob"):
		if b.QueueName != "" {
			return fmt.Sprintf("azure:storage:queue:%s", b.QueueName),
				"azure:storage:queue",
				b.QueueName
		}
		if b.Path != "" {
			return fmt.Sprintf("azure:storage:blob:%s", b.Path),
				"azure:storage:blob",
				b.Path
		}
	}
	return "", "", ""
}

type functionConfig struct {
	Bindings []functionBinding `json:"bindings"`
}

type functionBinding struct {
	Type         string `json:"type"`
	Direction    string `json:"direction"`
	Connection   string `json:"connection"`
	QueueName    string `json:"queueName"`
	TopicName    string `json:"topicName"`
	EventHubName string `json:"eventHubName"`
	Path         string `json:"path"`
}
