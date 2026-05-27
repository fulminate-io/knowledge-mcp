// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestPrivateEndpointCollector_Name(t *testing.T) {
	c := &privateEndpointCollector{}
	assert.Equal(t, "azure-private-endpoints", c.Name())
}

func TestPEEdges_SubnetAndService(t *testing.T) {
	peID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/pe-1"
	subnetID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/default"
	serviceID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Sql/servers/sql-1"

	pe := &armnetwork.PrivateEndpoint{
		ID: &peID,
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: &subnetID},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{
				{
					Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
						PrivateLinkServiceID: &serviceID,
					},
				},
			},
		},
	}

	edges := peEdges(pe)
	require.Len(t, edges, 2)

	assert.Equal(t, peID, edges[0].SourceID)
	assert.Equal(t, subnetID, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesSubnet, edges[0].Relationship)

	assert.Equal(t, peID, edges[1].SourceID)
	assert.Equal(t, serviceID, edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[1].Relationship)
}

func TestPEEdges_NilProperties(t *testing.T) {
	peID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/pe-1"
	pe := &armnetwork.PrivateEndpoint{
		ID:         &peID,
		Properties: nil,
	}
	edges := peEdges(pe)
	assert.Empty(t, edges)
}

func TestPEEdges_MultipleConnections(t *testing.T) {
	peID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/pe-1"
	svc1 := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/sa-1"
	svc2 := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/kv-1"

	pe := &armnetwork.PrivateEndpoint{
		ID: &peID,
		Properties: &armnetwork.PrivateEndpointProperties{
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{
				{
					Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
						PrivateLinkServiceID: &svc1,
					},
				},
				{
					Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
						PrivateLinkServiceID: &svc2,
					},
				},
			},
		},
	}

	edges := peEdges(pe)
	// No subnet edge (nil), two service edges.
	require.Len(t, edges, 2)
	assert.Equal(t, svc1, edges[0].TargetID)
	assert.Equal(t, svc2, edges[1].TargetID)
}

func TestPEEdges_ManualConnections(t *testing.T) {
	peID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/pe-1"
	serviceID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Sql/servers/sql-1"

	pe := &armnetwork.PrivateEndpoint{
		ID: &peID,
		Properties: &armnetwork.PrivateEndpointProperties{
			ManualPrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{
				{
					Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
						PrivateLinkServiceID: &serviceID,
					},
				},
			},
		},
	}

	edges := peEdges(pe)
	require.Len(t, edges, 1)
	assert.Equal(t, peID, edges[0].SourceID)
	assert.Equal(t, serviceID, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestPEResourceSpec(t *testing.T) {
	peID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/pe-1"
	name := "pe-1"
	location := "eastus"

	pe := &armnetwork.PrivateEndpoint{
		ID:       &peID,
		Name:     &name,
		Location: &location,
	}

	spec := peResourceSpec(pe, []byte(`{}`))
	assert.Equal(t, peID, spec.ID)
	assert.Equal(t, name, spec.Name)
	assert.Equal(t, "Microsoft.Network/privateEndpoints", spec.ResourceType)
	assert.Equal(t, location, spec.Region)
}
