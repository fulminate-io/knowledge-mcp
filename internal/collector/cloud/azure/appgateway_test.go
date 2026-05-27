// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestAppGatewayCollector_Name(t *testing.T) {
	c := &appGatewayCollector{}
	assert.Equal(t, "azure-appgateways", c.Name())
}

func TestAppGwEdges_NilProperties(t *testing.T) {
	gw := &armnetwork.ApplicationGateway{
		ID:         new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"),
		Properties: nil,
	}
	edges := appGwEdges(gw)
	assert.Nil(t, edges)
}

func TestAppGwEdges_SubnetFromIPConfig(t *testing.T) {
	subnetID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/appgw-subnet"
	gw := &armnetwork.ApplicationGateway{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"),
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			GatewayIPConfigurations: []*armnetwork.ApplicationGatewayIPConfiguration{
				{
					Properties: &armnetwork.ApplicationGatewayIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.SubResource{ID: &subnetID},
					},
				},
			},
		},
	}
	edges := appGwEdges(gw)
	require.Len(t, edges, 1)
	assert.Equal(t, *gw.ID, edges[0].SourceID)
	assert.Equal(t, subnetID, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesSubnet, edges[0].Relationship)
}

func TestAppGwBackendEdges(t *testing.T) {
	nicIPCfgID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic-1/ipConfigurations/ipcfg-1"
	gw := &armnetwork.ApplicationGateway{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"),
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			BackendAddressPools: []*armnetwork.ApplicationGatewayBackendAddressPool{
				{
					Properties: &armnetwork.ApplicationGatewayBackendAddressPoolPropertiesFormat{
						BackendIPConfigurations: []*armnetwork.InterfaceIPConfiguration{
							{ID: &nicIPCfgID},
						},
					},
				},
			},
		},
	}
	edges := appGwBackendEdges(gw)
	require.Len(t, edges, 1)
	assert.Equal(t, *gw.ID, edges[0].SourceID)
	assert.Equal(t, nicIPCfgID, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestAppGwBackendEdges_NilPool(t *testing.T) {
	gw := &armnetwork.ApplicationGateway{
		ID: new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"),
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			BackendAddressPools: []*armnetwork.ApplicationGatewayBackendAddressPool{
				{Properties: nil},
			},
		},
	}
	edges := appGwBackendEdges(gw)
	assert.Nil(t, edges)
}

func TestAppGwWAFProtectsEdge(t *testing.T) {
	gwID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"
	wafID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/ApplicationGatewayWebApplicationFirewallPolicies/waf-1"

	t.Run("emits PROTECTS when FirewallPolicy set", func(t *testing.T) {
		gw := &armnetwork.ApplicationGateway{
			ID: &gwID,
			Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
				FirewallPolicy: &armnetwork.SubResource{ID: &wafID},
			},
		}
		edges := appGwEdges(gw)
		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeProtects {
				assert.Equal(t, wafID, e.SourceID)
				assert.Equal(t, gwID, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected PROTECTS edge")
	})

	t.Run("no PROTECTS when no FirewallPolicy", func(t *testing.T) {
		gw := &armnetwork.ApplicationGateway{
			ID:         &gwID,
			Properties: &armnetwork.ApplicationGatewayPropertiesFormat{},
		}
		edges := appGwEdges(gw)
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeProtects, e.Relationship)
		}
	})
}

func TestAppGwSSLCertEdges(t *testing.T) {
	gwID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw-1"
	secretID := "https://myvault.vault.azure.net/secrets/cert-1/version1"

	t.Run("emits USES_CERT when KeyVaultSecretID set", func(t *testing.T) {
		gw := &armnetwork.ApplicationGateway{
			ID: &gwID,
			Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
				SSLCertificates: []*armnetwork.ApplicationGatewaySSLCertificate{{
					Properties: &armnetwork.ApplicationGatewaySSLCertificatePropertiesFormat{
						KeyVaultSecretID: &secretID,
					},
				}},
			},
		}
		edges := appGwSSLCertEdges(gw)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeUsesCert, edges[0].Relationship)
		assert.Equal(t, gwID, edges[0].SourceID)
		assert.Equal(t, secretID, edges[0].TargetID)
	})

	t.Run("no edge when no KeyVaultSecretID", func(t *testing.T) {
		gw := &armnetwork.ApplicationGateway{
			ID: &gwID,
			Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
				SSLCertificates: []*armnetwork.ApplicationGatewaySSLCertificate{{
					Properties: &armnetwork.ApplicationGatewaySSLCertificatePropertiesFormat{},
				}},
			},
		}
		edges := appGwSSLCertEdges(gw)
		assert.Empty(t, edges)
	})

	t.Run("handles multiple certificates", func(t *testing.T) {
		secret2 := "https://myvault.vault.azure.net/secrets/cert-2/version2"
		gw := &armnetwork.ApplicationGateway{
			ID: &gwID,
			Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
				SSLCertificates: []*armnetwork.ApplicationGatewaySSLCertificate{
					{Properties: &armnetwork.ApplicationGatewaySSLCertificatePropertiesFormat{
						KeyVaultSecretID: &secretID,
					}},
					{Properties: &armnetwork.ApplicationGatewaySSLCertificatePropertiesFormat{
						KeyVaultSecretID: &secret2,
					}},
				},
			},
		}
		edges := appGwSSLCertEdges(gw)
		require.Len(t, edges, 2)
	})
}
