// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFirewallCollector_Name(t *testing.T) {
	c := &firewallCollector{}
	assert.Equal(t, "azure-firewalls", c.Name())
}

func TestVnetIDFromSubnet(t *testing.T) {
	tests := []struct {
		name     string
		subnetID string
		want     string
	}{
		{
			"standard subnet path",
			"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/AzureFirewallSubnet",
			"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1",
		},
		{
			"case-insensitive subnets segment",
			"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/Subnets/default",
			"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1",
		},
		{
			"no subnets segment",
			"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1",
			"",
		},
		{
			"empty string",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, vnetIDFromSubnet(tt.subnetID))
		})
	}
}

func TestFirewallEdges_NilProperties(t *testing.T) {
	fw := &armnetwork.AzureFirewall{
		ID:         new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/azureFirewalls/fw-1"),
		Properties: nil,
	}
	edges := firewallEdges(fw)
	assert.Nil(t, edges)
}

func TestFirewallEdges_IPConfig(t *testing.T) {
	subnetID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/AzureFirewallSubnet"
	fw := &armnetwork.AzureFirewall{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/azureFirewalls/fw-1"),
		Properties: &armnetwork.AzureFirewallPropertiesFormat{
			IPConfigurations: []*armnetwork.AzureFirewallIPConfiguration{
				{
					Properties: &armnetwork.AzureFirewallIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.SubResource{ID: &subnetID},
					},
				},
			},
		},
	}
	edges := firewallEdges(fw)
	require.Len(t, edges, 2) // USES_SUBNET + USES_NETWORK

	assert.Equal(t, kgtypes.EdgeUsesSubnet, edges[0].Relationship)
	assert.Equal(t, subnetID, edges[0].TargetID)

	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[1].Relationship)
	assert.Contains(t, edges[1].TargetID, "virtualNetworks/vnet-1")
	assert.NotContains(t, edges[1].TargetID, "subnets")
}

func TestFirewallEdges_DeduplicatesSubnets(t *testing.T) {
	subnetID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/AzureFirewallSubnet"
	fw := &armnetwork.AzureFirewall{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/azureFirewalls/fw-1"),
		Properties: &armnetwork.AzureFirewallPropertiesFormat{
			IPConfigurations: []*armnetwork.AzureFirewallIPConfiguration{
				{
					Properties: &armnetwork.AzureFirewallIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.SubResource{ID: &subnetID},
					},
				},
				{
					Properties: &armnetwork.AzureFirewallIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.SubResource{ID: &subnetID},
					},
				},
			},
		},
	}
	edges := firewallEdges(fw)
	// Same subnet twice should produce only 1 USES_SUBNET + 1 USES_NETWORK.
	require.Len(t, edges, 2)
}

func TestFirewallEdges_ManagementIPConfig(t *testing.T) {
	ipSubnet := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/AzureFirewallSubnet"
	mgmtSubnet := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/AzureFirewallManagementSubnet"
	fw := &armnetwork.AzureFirewall{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/azureFirewalls/fw-1"),
		Properties: &armnetwork.AzureFirewallPropertiesFormat{
			IPConfigurations: []*armnetwork.AzureFirewallIPConfiguration{
				{
					Properties: &armnetwork.AzureFirewallIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.SubResource{ID: &ipSubnet},
					},
				},
			},
			ManagementIPConfiguration: &armnetwork.AzureFirewallIPConfiguration{
				Properties: &armnetwork.AzureFirewallIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.SubResource{ID: &mgmtSubnet},
				},
			},
		},
	}
	edges := firewallEdges(fw)
	// IP config: USES_SUBNET + USES_NETWORK (vnet deduped with mgmt since same vnet).
	// Mgmt config: USES_SUBNET only (vnet already seen).
	require.Len(t, edges, 3)

	subnetEdges := 0
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeUsesSubnet {
			subnetEdges++
		}
	}
	assert.Equal(t, 2, subnetEdges, "should have 2 USES_SUBNET edges (IP + mgmt)")
}

func TestFirewallDNATProtectsEdges(t *testing.T) {
	fwID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/azureFirewalls/fw-1"
	resID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm-1"

	t.Run("emits PROTECTS when translated target is resource ID", func(t *testing.T) {
		fw := &armnetwork.AzureFirewall{
			ID: &fwID,
			Properties: &armnetwork.AzureFirewallPropertiesFormat{
				NatRuleCollections: []*armnetwork.AzureFirewallNatRuleCollection{{
					Properties: &armnetwork.AzureFirewallNatRuleCollectionProperties{
						Rules: []*armnetwork.AzureFirewallNatRule{{
							TranslatedAddress: &resID,
						}},
					},
				}},
			},
		}
		edges := firewallDNATProtectsEdges(fw)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeProtects, edges[0].Relationship)
		assert.Equal(t, fwID, edges[0].SourceID)
		assert.Equal(t, resID, edges[0].TargetID)
	})

	t.Run("skips IP addresses", func(t *testing.T) {
		ip := "10.0.1.4"
		fw := &armnetwork.AzureFirewall{
			ID: &fwID,
			Properties: &armnetwork.AzureFirewallPropertiesFormat{
				NatRuleCollections: []*armnetwork.AzureFirewallNatRuleCollection{{
					Properties: &armnetwork.AzureFirewallNatRuleCollectionProperties{
						Rules: []*armnetwork.AzureFirewallNatRule{{
							TranslatedAddress: &ip,
						}},
					},
				}},
			},
		}
		edges := firewallDNATProtectsEdges(fw)
		assert.Empty(t, edges, "IP addresses should be skipped")
	})
}
