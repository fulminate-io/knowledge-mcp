// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestWebAppIdentityEdges(t *testing.T) {
	t.Run("nil identity", func(t *testing.T) {
		edges := webAppIdentityEdges("/sub/rg/site-1", nil)
		assert.Nil(t, edges)
	})

	t.Run("no user-assigned identities", func(t *testing.T) {
		identity := &armappservice.ManagedServiceIdentity{}
		edges := webAppIdentityEdges("/sub/rg/site-1", identity)
		assert.Nil(t, edges)
	})

	t.Run("single user-assigned identity", func(t *testing.T) {
		identity := &armappservice.ManagedServiceIdentity{
			UserAssignedIdentities: map[string]*armappservice.UserAssignedIdentity{
				"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/id-1": {},
			},
		}
		edges := webAppIdentityEdges("/sub/rg/site-1", identity)
		require.Len(t, edges, 1)
		assert.Equal(t, "/sub/rg/site-1", edges[0].SourceID)
		assert.Contains(t, edges[0].TargetID, "id-1")
		assert.Equal(t, kgtypes.EdgeAssumesRole, edges[0].Relationship)
	})

	t.Run("multiple user-assigned identities", func(t *testing.T) {
		identity := &armappservice.ManagedServiceIdentity{
			UserAssignedIdentities: map[string]*armappservice.UserAssignedIdentity{
				"/subscriptions/sub-1/rg/id-1": {},
				"/subscriptions/sub-1/rg/id-2": {},
			},
		}
		edges := webAppIdentityEdges("/sub/rg/site-1", identity)
		assert.Len(t, edges, 2)
		for _, e := range edges {
			assert.Equal(t, kgtypes.EdgeAssumesRole, e.Relationship)
		}
	})
}

func TestWebAppSubnetEdge(t *testing.T) {
	t.Run("nil properties", func(t *testing.T) {
		edge := webAppSubnetEdge("/sub/rg/site-1", nil)
		assert.Nil(t, edge)
	})

	t.Run("no subnet configured", func(t *testing.T) {
		props := &armappservice.SiteProperties{}
		edge := webAppSubnetEdge("/sub/rg/site-1", props)
		assert.Nil(t, edge)
	})

	t.Run("empty subnet ID", func(t *testing.T) {
		empty := ""
		props := &armappservice.SiteProperties{
			VirtualNetworkSubnetID: &empty,
		}
		edge := webAppSubnetEdge("/sub/rg/site-1", props)
		assert.Nil(t, edge)
	})

	t.Run("valid subnet integration", func(t *testing.T) {
		subnetID := "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1/subnets/sn-1"
		props := &armappservice.SiteProperties{
			VirtualNetworkSubnetID: &subnetID,
		}
		edge := webAppSubnetEdge("/sub/rg/site-1", props)
		require.NotNil(t, edge)
		assert.Equal(t, "/sub/rg/site-1", edge.SourceID)
		assert.Equal(t, subnetID, edge.TargetID)
		assert.Equal(t, kgtypes.EdgeUsesSubnet, edge.Relationship)
	})
}

func TestWebAppKeyVaultRefEdges(t *testing.T) {
	t.Run("no settings", func(t *testing.T) {
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", nil, map[string]bool{})
		assert.Nil(t, edges)
		assert.Nil(t, proxies)
	})

	t.Run("no key vault references", func(t *testing.T) {
		val := "just-a-value"
		settings := []*armappservice.NameValuePair{
			{Value: &val},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		assert.Nil(t, edges)
		assert.Nil(t, proxies)
	})

	t.Run("SecretUri format emits sentinel + proxy", func(t *testing.T) {
		val := "@Microsoft.KeyVault(SecretUri=https://myvault.vault.azure.net/secrets/mysecret/)"
		settings := []*armappservice.NameValuePair{
			{Value: &val},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		require.Len(t, edges, 1)
		assert.Equal(t, "/site-1", edges[0].SourceID)
		assert.Equal(t, "azure:keyvault:myvault", edges[0].TargetID)
		assert.Equal(t, kgtypes.EdgeMountsSecret, edges[0].Relationship)
		// Sentinel must NOT contain the broken '*' resourceGroups placeholder.
		assert.NotContains(t, edges[0].TargetID, "/resourceGroups/*/")

		require.Len(t, proxies, 1)
		assert.Equal(t, "azure:keyvault:myvault", proxies[0].ID)
		assert.Equal(t, "myvault", proxies[0].Name)
		assert.Equal(t, "azure:keyvault:vault", proxies[0].ResourceType)
		assert.Equal(t, "false", proxies[0].Metadata["collected"])
		assert.Equal(t, "sub-1", proxies[0].Metadata["subscription_id"])
	})

	t.Run("VaultName format", func(t *testing.T) {
		val := "@Microsoft.KeyVault(VaultName=othervault;SecretName=mysecret)"
		settings := []*armappservice.NameValuePair{
			{Value: &val},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		require.Len(t, edges, 1)
		assert.Equal(t, "azure:keyvault:othervault", edges[0].TargetID)
		require.Len(t, proxies, 1)
	})

	t.Run("deduplicates same vault within site", func(t *testing.T) {
		val1 := "@Microsoft.KeyVault(SecretUri=https://samevault.vault.azure.net/secrets/secret1/)"
		val2 := "@Microsoft.KeyVault(VaultName=samevault;SecretName=secret2)"
		settings := []*armappservice.NameValuePair{
			{Value: &val1},
			{Value: &val2},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		require.Len(t, edges, 1, "same vault referenced twice should produce one edge")
		require.Len(t, proxies, 1)
	})

	t.Run("seenProxies dedupes across sites", func(t *testing.T) {
		val := "@Microsoft.KeyVault(VaultName=shared;SecretName=s)"
		settings := []*armappservice.NameValuePair{{Value: &val}}
		seen := map[string]bool{}
		edges1, proxies1 := webAppKeyVaultRefEdges("/site-a", "sub-1", settings, seen)
		edges2, proxies2 := webAppKeyVaultRefEdges("/site-b", "sub-1", settings, seen)

		require.Len(t, edges1, 1)
		require.Len(t, proxies1, 1, "first site emits the proxy")
		require.Len(t, edges2, 1, "second site still emits its own edge")
		assert.Empty(t, proxies2, "shared seenProxies suppresses duplicate proxy")
	})

	t.Run("multiple vaults", func(t *testing.T) {
		val1 := "@Microsoft.KeyVault(SecretUri=https://vault-a.vault.azure.net/secrets/s1/)"
		val2 := "@Microsoft.KeyVault(VaultName=vault-b;SecretName=s2)"
		plain := "not-a-kv-ref"
		settings := []*armappservice.NameValuePair{
			{Value: &val1},
			{Value: &val2},
			{Value: &plain},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		assert.Len(t, edges, 2)
		assert.Len(t, proxies, 2)
	})

	t.Run("nil value in settings", func(t *testing.T) {
		settings := []*armappservice.NameValuePair{
			{Value: nil},
		}
		edges, proxies := webAppKeyVaultRefEdges("/site-1", "sub-1", settings, map[string]bool{})
		assert.Nil(t, edges)
		assert.Nil(t, proxies)
	})
}

func TestExtractKVRefVaultName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"SecretUri format",
			"@Microsoft.KeyVault(SecretUri=https://myvault.vault.azure.net/secrets/mysecret/)",
			"myvault",
		},
		{
			"VaultName format",
			"@Microsoft.KeyVault(VaultName=myvault;SecretName=mysecret)",
			"myvault",
		},
		{
			"regular value",
			"some-config-value",
			"",
		},
		{
			"empty string",
			"",
			"",
		},
		{
			"SecretUri with version",
			"@Microsoft.KeyVault(SecretUri=https://prod-vault.vault.azure.net/secrets/db-password/abc123)",
			"prod-vault",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractKVRefVaultName(tt.input))
		})
	}
}
