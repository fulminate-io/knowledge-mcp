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

type privateEndpointCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newPrivateEndpointCollector(cred azcore.TokenCredential, subID string) *privateEndpointCollector {
	return &privateEndpointCollector{cred: cred, subscriptionID: subID}
}

func (c *privateEndpointCollector) Name() string { return "azure-private-endpoints" }

func (c *privateEndpointCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewPrivateEndpointsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-private-endpoints: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-private-endpoints: list: %w", err)
		}

		for _, pe := range page.Value {
			if pe.ID == nil || pe.Name == nil {
				continue
			}

			content, err := json.Marshal(pe)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, peResourceSpec(pe, content))
			result.Edges = append(result.Edges, peEdges(pe)...)
		}
	}

	return result, nil
}

func peResourceSpec(pe *armnetwork.PrivateEndpoint, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *pe.ID,
		Name:         *pe.Name,
		ResourceType: "Microsoft.Network/privateEndpoints",
		Content:      content,
	}
	if pe.Location != nil {
		spec.Region = *pe.Location
	}
	return spec
}

func peEdges(pe *armnetwork.PrivateEndpoint) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	if pe.Properties == nil {
		return edges
	}

	// PE → subnet (USES_SUBNET)
	if pe.Properties.Subnet != nil && pe.Properties.Subnet.ID != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *pe.ID,
			TargetID:     *pe.Properties.Subnet.ID,
			Relationship: kgtypes.EdgeUsesSubnet,
		})
	}

	// PE → PaaS service (TARGETS) via privateLinkServiceConnections
	for _, conn := range pe.Properties.PrivateLinkServiceConnections {
		if conn.Properties != nil && conn.Properties.PrivateLinkServiceID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *pe.ID,
				TargetID:     *conn.Properties.PrivateLinkServiceID,
				Relationship: kgtypes.EdgeTargets,
			})
		}
	}

	// Also check manual connections (same structure, different approval flow).
	for _, conn := range pe.Properties.ManualPrivateLinkServiceConnections {
		if conn.Properties != nil && conn.Properties.PrivateLinkServiceID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *pe.ID,
				TargetID:     *conn.Properties.PrivateLinkServiceID,
				Relationship: kgtypes.EdgeTargets,
			})
		}
	}

	return edges
}
