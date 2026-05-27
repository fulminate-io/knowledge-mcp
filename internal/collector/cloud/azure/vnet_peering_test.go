// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestVNetPeeringEdges(t *testing.T) {
	localVNetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1"
	remoteVNetID := "/subscriptions/sub/resourceGroups/rg2/providers/Microsoft.Network/virtualNetworks/vnet-2"

	t.Run("emits bidirectional PEERED_WITH", func(t *testing.T) {
		vnet := &armnetwork.VirtualNetwork{ID: &localVNetID}
		peering := &armnetwork.VirtualNetworkPeering{
			ID:   new(localVNetID + "/virtualNetworkPeerings/peer-to-vnet2"),
			Name: new("peer-to-vnet2"),
			Properties: &armnetwork.VirtualNetworkPeeringPropertiesFormat{
				RemoteVirtualNetwork: &armnetwork.SubResource{ID: &remoteVNetID},
			},
		}
		edges := vnetPeeringEdges(peering, vnet)
		require.Len(t, edges, 2)

		assert.Equal(t, kgtypes.EdgePeeredWith, edges[0].Relationship)
		assert.Equal(t, localVNetID, edges[0].SourceID)
		assert.Equal(t, remoteVNetID, edges[0].TargetID)

		assert.Equal(t, kgtypes.EdgePeeredWith, edges[1].Relationship)
		assert.Equal(t, remoteVNetID, edges[1].SourceID)
		assert.Equal(t, localVNetID, edges[1].TargetID)
	})

	t.Run("no edges when no remote VNet", func(t *testing.T) {
		vnet := &armnetwork.VirtualNetwork{ID: &localVNetID}
		peering := &armnetwork.VirtualNetworkPeering{
			ID:         new(localVNetID + "/virtualNetworkPeerings/peer-1"),
			Name:       new("peer-1"),
			Properties: &armnetwork.VirtualNetworkPeeringPropertiesFormat{},
		}
		edges := vnetPeeringEdges(peering, vnet)
		assert.Empty(t, edges)
	})
}
