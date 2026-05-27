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

type natGatewayCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newNatGatewayCollector(cred azcore.TokenCredential, subID string) *natGatewayCollector {
	return &natGatewayCollector{cred: cred, subscriptionID: subID}
}

func (c *natGatewayCollector) Name() string { return "azure-natgateways" }

func (c *natGatewayCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewNatGatewaysClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-natgateways: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-natgateways: list: %w", err)
		}

		for _, ng := range page.Value {
			if ng.ID == nil || ng.Name == nil {
				continue
			}

			content, err := json.Marshal(ng)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, natGatewayResourceSpec(ng, content))
			result.Edges = append(result.Edges, natGatewayEdges(ng)...)
		}
	}

	return result, nil
}

func natGatewayResourceSpec(ng *armnetwork.NatGateway, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ng.ID,
		Name:         *ng.Name,
		ResourceType: "Microsoft.Network/natGateways",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ng.Location != nil {
		spec.Region = *ng.Location
	}
	if ng.SKU != nil && ng.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*ng.SKU.Name)
	}
	if ng.Properties != nil {
		if ng.Properties.IdleTimeoutInMinutes != nil {
			spec.Metadata["idleTimeoutInMinutes"] = fmt.Sprintf("%d", *ng.Properties.IdleTimeoutInMinutes)
		}
		if ng.Properties.ProvisioningState != nil {
			spec.Metadata["provisioningState"] = string(*ng.Properties.ProvisioningState)
		}
	}
	return spec
}

// natGatewayEdges emits USES_SUBNET edges for every subnet the NAT gateway is
// associated with and a derived USES_NETWORK edge to the parent VNet for each
// distinct VNet. Associated subnets come from Properties.Subnets (populated by
// the service as a read-only back-reference from the subnet's NAT gateway ID).
func natGatewayEdges(ng *armnetwork.NatGateway) []cloud.EdgeSpec {
	if ng.Properties == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	seen := map[string]bool{}

	for _, sub := range ng.Properties.Subnets {
		if sub == nil || sub.ID == nil || *sub.ID == "" {
			continue
		}
		subnetID := *sub.ID
		if !seen[subnetID] {
			seen[subnetID] = true
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ng.ID,
				TargetID:     subnetID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
		if vnetID := vnetIDFromSubnet(subnetID); vnetID != "" && !seen[vnetID] {
			seen[vnetID] = true
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ng.ID,
				TargetID:     vnetID,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	return edges
}
