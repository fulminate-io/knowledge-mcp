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

type vnetCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newVNetCollector(cred azcore.TokenCredential, subID string) *vnetCollector {
	return &vnetCollector{cred: cred, subscriptionID: subID}
}

func (c *vnetCollector) Name() string { return "azure-vnets" }

func (c *vnetCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewVirtualNetworksClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vnets: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-vnets: list: %w", err)
		}

		for _, vnet := range page.Value {
			if vnet.ID == nil || vnet.Name == nil {
				continue
			}

			content, err := json.Marshal(vnet)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, vnetResourceSpec(vnet, content))
			vnetCollectSubnets(vnet, &result)
		}
	}

	return result, nil
}

func vnetResourceSpec(vnet *armnetwork.VirtualNetwork, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *vnet.ID,
		Name:         *vnet.Name,
		ResourceType: "Microsoft.Network/virtualNetworks",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vnet.Location != nil {
		spec.Region = *vnet.Location
	}
	if vnet.Properties != nil && vnet.Properties.AddressSpace != nil {
		for i, prefix := range vnet.Properties.AddressSpace.AddressPrefixes {
			if prefix != nil {
				spec.Metadata[fmt.Sprintf("addressPrefix_%d", i)] = *prefix
			}
		}
	}
	return spec
}

func vnetCollectSubnets(vnet *armnetwork.VirtualNetwork, result *cloud.SubCollectorResult) {
	if vnet.Properties == nil {
		return
	}
	for _, subnet := range vnet.Properties.Subnets {
		if subnet.ID == nil || subnet.Name == nil {
			continue
		}

		subnetContent, err := json.Marshal(subnet)
		if err != nil {
			continue
		}

		result.Resources = append(result.Resources, subnetResourceSpec(vnet, subnet, subnetContent))
		result.Edges = append(result.Edges, subnetEdges(vnet, subnet)...)
	}
}

func subnetResourceSpec(vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *subnet.ID,
		Name:         *subnet.Name,
		ResourceType: "Microsoft.Network/virtualNetworks/subnets",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vnet.Location != nil {
		spec.Region = *vnet.Location
	}
	if subnet.Properties != nil && subnet.Properties.AddressPrefix != nil {
		spec.Metadata["addressPrefix"] = *subnet.Properties.AddressPrefix
	}
	return spec
}

func subnetEdges(vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet) []cloud.EdgeSpec {
	edges := []cloud.EdgeSpec{
		{
			SourceID:     *subnet.ID,
			TargetID:     *vnet.ID,
			Relationship: kgtypes.EdgeUsesNetwork,
		},
	}
	if subnet.Properties != nil && subnet.Properties.NetworkSecurityGroup != nil && subnet.Properties.NetworkSecurityGroup.ID != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *subnet.ID,
			TargetID:     *subnet.Properties.NetworkSecurityGroup.ID,
			Relationship: kgtypes.EdgeUsesSecurityGroup,
		})
	}
	return edges
}
