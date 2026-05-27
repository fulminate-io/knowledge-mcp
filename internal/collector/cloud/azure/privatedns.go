// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type privateDNSCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newPrivateDNSCollector(cred azcore.TokenCredential, subID string) *privateDNSCollector {
	return &privateDNSCollector{cred: cred, subscriptionID: subID}
}

func (c *privateDNSCollector) Name() string { return "azure-private-dns" }

func (c *privateDNSCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	zonesClient, err := armprivatedns.NewPrivateZonesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-private-dns: zones client: %w", err)
	}
	linksClient, err := armprivatedns.NewVirtualNetworkLinksClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-private-dns: links client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := zonesClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-private-dns: list zones: %w", err)
		}
		for _, zone := range page.Value {
			if zone.ID == nil || zone.Name == nil {
				continue
			}
			content, err := json.Marshal(zone)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, cloud.ResourceSpec{
				ID:           *zone.ID,
				Name:         *zone.Name,
				ResourceType: "Microsoft.Network/privateDnsZones",
				Content:      content,
			})

			// Zone → VNet links (USES_NETWORK)
			linkEdges := c.collectVNetLinks(ctx, linksClient, *zone.ID, *zone.Name)
			result.Edges = append(result.Edges, linkEdges...)
		}
	}

	return result, nil
}

func (c *privateDNSCollector) collectVNetLinks(
	ctx context.Context,
	client *armprivatedns.VirtualNetworkLinksClient,
	zoneID, zoneName string,
) []cloud.EdgeSpec {
	rg := extractResourceGroup(zoneID)
	if rg == "" {
		return nil
	}

	var edges []cloud.EdgeSpec
	pager := client.NewListPager(rg, zoneName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, link := range page.Value {
			if link.Properties == nil || link.Properties.VirtualNetwork == nil {
				continue
			}
			if vnetID := link.Properties.VirtualNetwork.ID; vnetID != nil && *vnetID != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     zoneID,
					TargetID:     *vnetID,
					Relationship: kgtypes.EdgeUsesNetwork,
				})
			}
		}
	}
	return edges
}

// extractResourceGroup extracts the resource group name from an Azure
// resource ID.
func extractResourceGroup(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}
