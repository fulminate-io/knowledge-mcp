// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestRedisEdges(t *testing.T) {
	cacheID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cache/redis/mycache"
	subnetID := "/subscriptions/sub/resourceGroups/net-rg/providers/Microsoft.Network/virtualNetworks/vnet1/subnets/redis-subnet"
	expectedVnetID := "/subscriptions/sub/resourceGroups/net-rg/providers/Microsoft.Network/virtualNetworks/vnet1"
	identityID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami"

	t.Run("emits USES_SUBNET and USES_NETWORK when SubnetID set", func(t *testing.T) {
		sid := subnetID
		cache := &armredis.ResourceInfo{
			ID: &cacheID,
			Properties: &armredis.Properties{
				SubnetID: &sid,
			},
		}
		edges := redisEdges(cache)

		var foundSubnet, foundNet bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet {
				assert.Equal(t, cacheID, e.SourceID)
				assert.Equal(t, subnetID, e.TargetID)
				foundSubnet = true
			}
			if e.Relationship == kgtypes.EdgeUsesNetwork {
				assert.Equal(t, cacheID, e.SourceID)
				assert.Equal(t, expectedVnetID, e.TargetID)
				foundNet = true
			}
		}
		assert.True(t, foundSubnet, "expected EdgeUsesSubnet")
		assert.True(t, foundNet, "expected EdgeUsesNetwork")
	})

	t.Run("emits ASSUMES_ROLE for user-assigned identity", func(t *testing.T) {
		cache := &armredis.ResourceInfo{
			ID: &cacheID,
			Identity: &armredis.ManagedServiceIdentity{
				UserAssignedIdentities: map[string]*armredis.UserAssignedIdentity{
					identityID: {},
				},
			},
		}
		edges := redisEdges(cache)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeAssumesRole {
				assert.Equal(t, cacheID, e.SourceID)
				assert.Equal(t, identityID, e.TargetID)
				assert.Equal(t, "managed_identity", e.Metadata["role_source"])
				found = true
			}
		}
		assert.True(t, found, "expected EdgeAssumesRole")
	})

	t.Run("emits no edges when Properties and Identity nil", func(t *testing.T) {
		cache := &armredis.ResourceInfo{ID: &cacheID}
		edges := redisEdges(cache)
		assert.Empty(t, edges)
	})

	t.Run("emits no ENCRYPTS_WITH edge (no CMK support)", func(t *testing.T) {
		sid := subnetID
		cache := &armredis.ResourceInfo{
			ID: &cacheID,
			Properties: &armredis.Properties{
				SubnetID: &sid,
			},
		}
		edges := redisEdges(cache)
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}
