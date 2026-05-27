// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type vnetPeeringCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newVNetPeeringCollector(cred azcore.TokenCredential, subID string) *vnetPeeringCollector {
	return &vnetPeeringCollector{cred: cred, subscriptionID: subID}
}

func (c *vnetPeeringCollector) Name() string { return "azure-vnet-peering" }

// Collect discovers VNet peerings by listing all VNets and then listing
// peerings per VNet. Each peering emits a bidirectional PEERED_WITH edge.
func (c *vnetPeeringCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	vnetClient, err := armnetwork.NewVirtualNetworksClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vnet-peering: vnet client: %w", err)
	}

	peeringClient, err := armnetwork.NewVirtualNetworkPeeringsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vnet-peering: peering client: %w", err)
	}

	var result cloud.SubCollectorResult

	vnetPager := vnetClient.NewListAllPager(nil)
	for vnetPager.More() {
		page, err := vnetPager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-vnet-peering: list vnets: %w", err)
		}
		for _, vnet := range page.Value {
			if vnet.ID == nil || vnet.Name == nil {
				continue
			}
			rg := parseResourceGroup(*vnet.ID)
			if rg == "" {
				continue
			}
			c.collectPeerings(ctx, peeringClient, rg, vnet, &result)
		}
	}

	return result, nil
}

func (c *vnetPeeringCollector) collectPeerings(
	ctx context.Context,
	client *armnetwork.VirtualNetworkPeeringsClient,
	resourceGroup string,
	vnet *armnetwork.VirtualNetwork,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListPager(resourceGroup, *vnet.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, peering := range page.Value {
			if peering.ID == nil || peering.Name == nil {
				continue
			}
			content, err := json.Marshal(peering)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, vnetPeeringResourceSpec(peering, vnet, content))
			result.Edges = append(result.Edges, vnetPeeringEdges(peering, vnet)...)
		}
	}
}

func vnetPeeringResourceSpec(
	peering *armnetwork.VirtualNetworkPeering,
	vnet *armnetwork.VirtualNetwork,
	content []byte,
) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *peering.ID,
		Name:         *peering.Name,
		ResourceType: "Microsoft.Network/virtualNetworks/virtualNetworkPeerings",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vnet.Location != nil {
		spec.Region = *vnet.Location
	}
	return spec
}

// vnetPeeringEdges emits bidirectional PEERED_WITH edges between the local
// and remote VNets. Both directions are emitted so the relationship is
// traversable from either side.
func vnetPeeringEdges(
	peering *armnetwork.VirtualNetworkPeering,
	vnet *armnetwork.VirtualNetwork,
) []cloud.EdgeSpec {
	if peering.Properties == nil || peering.Properties.RemoteVirtualNetwork == nil {
		return nil
	}
	remoteID := peering.Properties.RemoteVirtualNetwork.ID
	if remoteID == nil || *remoteID == "" {
		return nil
	}
	return []cloud.EdgeSpec{
		{
			SourceID:     *vnet.ID,
			TargetID:     *remoteID,
			Relationship: kgtypes.EdgePeeredWith,
		},
		{
			SourceID:     *remoteID,
			TargetID:     *vnet.ID,
			Relationship: kgtypes.EdgePeeredWith,
		},
	}
}
