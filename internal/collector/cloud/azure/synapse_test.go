// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSynapseWorkspaceEdges(t *testing.T) {
	wsID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Synapse/workspaces/myws"
	subnetID := "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet1/subnets/compute"
	vnetID := "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet1"
	identityID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami"
	kvURL := "https://myvault.vault.azure.net/keys/workspace-cmk"

	t.Run("emits USES_SUBNET and USES_NETWORK for compute subnet", func(t *testing.T) {
		sid := subnetID
		ws := &armsynapse.Workspace{
			ID: &wsID,
			Properties: &armsynapse.WorkspaceProperties{
				VirtualNetworkProfile: &armsynapse.VirtualNetworkProfile{
					ComputeSubnetID: &sid,
				},
			},
		}
		edges := synapseWorkspaceEdges(ws)

		var foundSubnet, foundNet bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet && e.TargetID == subnetID {
				foundSubnet = true
			}
			if e.Relationship == kgtypes.EdgeUsesNetwork && e.TargetID == vnetID {
				foundNet = true
			}
		}
		assert.True(t, foundSubnet, "expected USES_SUBNET")
		assert.True(t, foundNet, "expected USES_NETWORK")
	})

	t.Run("emits ENCRYPTS_WITH for CMK key vault URL", func(t *testing.T) {
		kv := kvURL
		ws := &armsynapse.Workspace{
			ID: &wsID,
			Properties: &armsynapse.WorkspaceProperties{
				Encryption: &armsynapse.EncryptionDetails{
					Cmk: &armsynapse.CustomerManagedKeyDetails{
						Key: &armsynapse.WorkspaceKeyDetails{KeyVaultURL: &kv},
					},
				},
			},
		}
		edges := synapseWorkspaceEdges(ws)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith && e.TargetID == kvURL {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("emits ASSUMES_ROLE for user-assigned identity", func(t *testing.T) {
		ws := &armsynapse.Workspace{
			ID: &wsID,
			Identity: &armsynapse.ManagedIdentity{
				UserAssignedIdentities: map[string]*armsynapse.UserAssignedManagedIdentity{
					identityID: {},
				},
			},
		}
		edges := synapseWorkspaceEdges(ws)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeAssumesRole && e.TargetID == identityID {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("returns empty when all fields nil", func(t *testing.T) {
		ws := &armsynapse.Workspace{ID: &wsID}
		edges := synapseWorkspaceEdges(ws)
		assert.Empty(t, edges)
	})
}

func TestParseSynapseWorkspaceID(t *testing.T) {
	rg, name := parseSynapseWorkspaceID("/subscriptions/sub/resourceGroups/myRG/providers/Microsoft.Synapse/workspaces/myws")
	assert.Equal(t, "myRG", rg)
	assert.Equal(t, "myws", name)

	rg, name = parseSynapseWorkspaceID("/bogus")
	assert.Empty(t, rg)
	assert.Empty(t, name)
}
