// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestNatGatewayEdges(t *testing.T) {
	ngID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/natGateways/myng"
	subnetA := "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet1/subnets/sub-a"
	subnetB := "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet1/subnets/sub-b"
	vnetID := "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet1"

	t.Run("emits USES_SUBNET for each associated subnet", func(t *testing.T) {
		saA := subnetA
		saB := subnetB
		ng := &armnetwork.NatGateway{
			ID: &ngID,
			Properties: &armnetwork.NatGatewayPropertiesFormat{
				Subnets: []*armnetwork.SubResource{
					{ID: &saA},
					{ID: &saB},
				},
			},
		}
		edges := natGatewayEdges(ng)

		var subnetEdges int
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet {
				assert.Equal(t, ngID, e.SourceID)
				subnetEdges++
			}
		}
		assert.Equal(t, 2, subnetEdges, "expected 2 EdgeUsesSubnet edges")
	})

	t.Run("emits single USES_NETWORK for same parent VNet", func(t *testing.T) {
		saA := subnetA
		saB := subnetB
		ng := &armnetwork.NatGateway{
			ID: &ngID,
			Properties: &armnetwork.NatGatewayPropertiesFormat{
				Subnets: []*armnetwork.SubResource{
					{ID: &saA},
					{ID: &saB},
				},
			},
		}
		edges := natGatewayEdges(ng)

		var netEdges int
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesNetwork {
				assert.Equal(t, ngID, e.SourceID)
				assert.Equal(t, vnetID, e.TargetID)
				netEdges++
			}
		}
		assert.Equal(t, 1, netEdges, "expected single deduplicated EdgeUsesNetwork")
	})

	t.Run("returns nil when Properties nil", func(t *testing.T) {
		ng := &armnetwork.NatGateway{ID: &ngID}
		edges := natGatewayEdges(ng)
		assert.Nil(t, edges)
	})

	t.Run("skips subnets with nil IDs", func(t *testing.T) {
		saA := subnetA
		ng := &armnetwork.NatGateway{
			ID: &ngID,
			Properties: &armnetwork.NatGatewayPropertiesFormat{
				Subnets: []*armnetwork.SubResource{
					{ID: &saA},
					{ID: nil},
					nil,
				},
			},
		}
		edges := natGatewayEdges(ng)

		var subnetEdges int
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet {
				subnetEdges++
			}
		}
		assert.Equal(t, 1, subnetEdges)
	})
}
