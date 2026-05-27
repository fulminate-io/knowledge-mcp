// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type eventHubCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newEventHubCollector(cred azcore.TokenCredential, subID string) *eventHubCollector {
	return &eventHubCollector{cred: cred, subscriptionID: subID}
}

func (c *eventHubCollector) Name() string { return "azure-eventhubs" }

func (c *eventHubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	nsClient, err := armeventhub.NewNamespacesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-eventhubs: ns client: %w", err)
	}

	ehClient, err := armeventhub.NewEventHubsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-eventhubs: eh client: %w", err)
	}

	cgClient, err := armeventhub.NewConsumerGroupsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-eventhubs: cg client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := nsClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-eventhubs: list: %w", err)
		}

		for _, ns := range page.Value {
			if ns.ID == nil || ns.Name == nil {
				continue
			}

			content, err := json.Marshal(ns)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, ehNamespaceResourceSpec(ns, content))
			result.Edges = append(result.Edges, ehNamespaceVNetEdges(ctx, nsClient, ns)...)
			result.Edges = append(result.Edges, ehNamespaceEncryptionEdges(ns)...)

			rg := parseResourceGroup(*ns.ID)
			if rg == "" {
				continue
			}
			c.collectEventHubs(ctx, ehClient, cgClient, rg, ns, &result)
		}
	}

	return result, nil
}

// collectEventHubs lists event hubs under a namespace and emits resources,
// CONTAINS edges, and capture SINKS_TO edges.
func (c *eventHubCollector) collectEventHubs(
	ctx context.Context,
	ehClient *armeventhub.EventHubsClient,
	cgClient *armeventhub.ConsumerGroupsClient,
	resourceGroup string,
	ns *armeventhub.EHNamespace,
	result *cloud.SubCollectorResult,
) {
	pager := ehClient.NewListByNamespacePager(resourceGroup, *ns.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, eh := range page.Value {
			if eh.ID == nil || eh.Name == nil {
				continue
			}
			content, err := json.Marshal(eh)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, ehResourceSpec(eh, ns, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ns.ID,
				TargetID:     *eh.ID,
				Relationship: kgtypes.EdgeContains,
			})
			result.Edges = append(result.Edges, ehCaptureEdge(eh)...)
			c.collectConsumerGroups(ctx, cgClient, resourceGroup, ns, eh, result)
		}
	}
}

// collectConsumerGroups lists consumer groups under an event hub and emits
// each as a resource with SUBSCRIBES_TO (consumer group → event hub) edge.
func (c *eventHubCollector) collectConsumerGroups(
	ctx context.Context,
	cgClient *armeventhub.ConsumerGroupsClient,
	resourceGroup string,
	ns *armeventhub.EHNamespace,
	eh *armeventhub.Eventhub,
	result *cloud.SubCollectorResult,
) {
	pager := cgClient.NewListByEventHubPager(resourceGroup, *ns.Name, *eh.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, cg := range page.Value {
			if cg.ID == nil || cg.Name == nil {
				continue
			}
			content, err := json.Marshal(cg)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, ehConsumerGroupResourceSpec(cg, ns, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *cg.ID,
				TargetID:     *eh.ID,
				Relationship: kgtypes.EdgeSubscribesTo,
			})
		}
	}
}

func ehNamespaceResourceSpec(ns *armeventhub.EHNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ns.ID,
		Name:         *ns.Name,
		ResourceType: "Microsoft.EventHub/namespaces",
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

func ehResourceSpec(eh *armeventhub.Eventhub, ns *armeventhub.EHNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *eh.ID,
		Name:         *eh.Name,
		ResourceType: "Microsoft.EventHub/namespaces/eventhubs",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	return spec
}

func ehConsumerGroupResourceSpec(cg *armeventhub.ConsumerGroup, ns *armeventhub.EHNamespace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *cg.ID,
		Name:         *cg.Name,
		ResourceType: "Microsoft.EventHub/namespaces/eventhubs/consumergroups",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ns.Location != nil {
		spec.Region = *ns.Location
	}
	return spec
}

// ehNamespaceVNetEdges fetches the network rule set for a namespace and emits
// USES_SUBNET edges for each virtual network rule.
func ehNamespaceVNetEdges(
	ctx context.Context,
	nsClient *armeventhub.NamespacesClient,
	ns *armeventhub.EHNamespace,
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
