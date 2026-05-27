// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type serviceBusCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newServiceBusCollector(cred azcore.TokenCredential, subID string) *serviceBusCollector {
	return &serviceBusCollector{cred: cred, subscriptionID: subID}
}

func (c *serviceBusCollector) Name() string { return "azure-servicebus" }

func (c *serviceBusCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	nsClient, err := armservicebus.NewNamespacesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-servicebus: ns client: %w", err)
	}

	queuesClient, err := armservicebus.NewQueuesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-servicebus: queues client: %w", err)
	}

	topicsClient, err := armservicebus.NewTopicsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-servicebus: topics client: %w", err)
	}

	subsClient, err := armservicebus.NewSubscriptionsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-servicebus: subscriptions client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := nsClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-servicebus: list namespaces: %w", err)
		}

		for _, ns := range page.Value {
			if ns.ID == nil || ns.Name == nil {
				continue
			}

			content, err := json.Marshal(ns)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, sbNamespaceResourceSpec(ns, content))
			result.Edges = append(result.Edges, sbNamespaceVNetEdges(ctx, nsClient, ns)...)
			result.Edges = append(result.Edges, sbNamespaceEncryptionEdges(ns)...)

			rg := parseResourceGroup(*ns.ID)
			if rg == "" {
				continue
			}
			c.collectQueues(ctx, queuesClient, rg, ns, &result)
			c.collectTopics(ctx, topicsClient, subsClient, rg, ns, &result)
		}
	}

	return result, nil
}

func sbNamespaceResourceSpec(ns *armservicebus.SBNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ns.ID,
		Name:         *ns.Name,
		ResourceType: "Microsoft.ServiceBus/namespaces",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	if ns.SKU != nil && ns.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*ns.SKU.Name)
	}
	if ns.SKU != nil && ns.SKU.Tier != nil {
		spec.Metadata["skuTier"] = string(*ns.SKU.Tier)
	}
	return spec
}

// sbNamespaceVNetEdges fetches the network rule set for a namespace and emits
// USES_SUBNET edges for each virtual network rule.
func sbNamespaceVNetEdges(
	ctx context.Context,
	nsClient *armservicebus.NamespacesClient,
	ns *armservicebus.SBNamespace,
) []cloud.EdgeSpec {
	rg := parseResourceGroup(*ns.ID)
	if rg == "" {
		return nil
	}

	resp, err := nsClient.GetNetworkRuleSet(ctx, rg, *ns.Name, nil)
	if err != nil {
		return nil // best-effort — skip on error
	}

	if resp.Properties == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, rule := range resp.Properties.VirtualNetworkRules {
		if rule.Subnet != nil && rule.Subnet.ID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ns.ID,
				TargetID:     *rule.Subnet.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}
	return edges
}

func (c *serviceBusCollector) collectQueues(
	ctx context.Context,
	client *armservicebus.QueuesClient,
	resourceGroup string,
	ns *armservicebus.SBNamespace,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByNamespacePager(resourceGroup, *ns.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, q := range page.Value {
			if q.ID == nil || q.Name == nil {
				continue
			}
			content, err := json.Marshal(q)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, sbQueueResourceSpec(q, ns, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ns.ID,
				TargetID:     *q.ID,
				Relationship: kgtypes.EdgeContains,
			})
			result.Edges = append(result.Edges, sbQueueDeadLetterEdge(q)...)
		}
	}
}

func (c *serviceBusCollector) collectTopics(
	ctx context.Context,
	client *armservicebus.TopicsClient,
	subsClient *armservicebus.SubscriptionsClient,
	resourceGroup string,
	ns *armservicebus.SBNamespace,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByNamespacePager(resourceGroup, *ns.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, t := range page.Value {
			if t.ID == nil || t.Name == nil {
				continue
			}
			content, err := json.Marshal(t)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, sbTopicResourceSpec(t, ns, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ns.ID,
				TargetID:     *t.ID,
				Relationship: kgtypes.EdgeContains,
			})
			c.collectSubscriptions(ctx, subsClient, resourceGroup, ns, t, result)
		}
	}
}

func sbQueueResourceSpec(q *armservicebus.SBQueue, ns *armservicebus.SBNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *q.ID,
		Name:         *q.Name,
		ResourceType: "Microsoft.ServiceBus/namespaces/queues",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	return spec
}

func sbTopicResourceSpec(t *armservicebus.SBTopic, ns *armservicebus.SBNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *t.ID,
		Name:         *t.Name,
		ResourceType: "Microsoft.ServiceBus/namespaces/topics",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	return spec
}

// parseResourceGroup extracts the resource group name from an Azure resource ID.
// IDs follow: /subscriptions/{sub}/resourceGroups/{rg}/providers/...
func parseResourceGroup(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}
