// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/search/armsearch"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSearchEdges(t *testing.T) {
	svcID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Search/searchServices/mysvc"
	identityID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami"
	peID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/privateEndpoints/mype"

	t.Run("emits ASSUMES_ROLE for user-assigned identity", func(t *testing.T) {
		svc := &armsearch.Service{
			ID: &svcID,
			Identity: &armsearch.Identity{
				UserAssignedIdentities: map[string]*armsearch.UserAssignedIdentity{
					identityID: {},
				},
			},
		}
		edges := searchEdges(svc)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeAssumesRole {
				assert.Equal(t, svcID, e.SourceID)
				assert.Equal(t, identityID, e.TargetID)
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("emits USES_SUBNET for each private endpoint", func(t *testing.T) {
		pid := peID
		svc := &armsearch.Service{
			ID: &svcID,
			Properties: &armsearch.ServiceProperties{
				PrivateEndpointConnections: []*armsearch.PrivateEndpointConnection{
					{
						Properties: &armsearch.PrivateEndpointConnectionProperties{
							PrivateEndpoint: &armsearch.PrivateEndpointConnectionPropertiesPrivateEndpoint{
								ID: &pid,
							},
						},
					},
				},
			},
		}
		edges := searchEdges(svc)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet {
				assert.Equal(t, svcID, e.SourceID)
				assert.Equal(t, peID, e.TargetID)
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("returns empty when all fields nil", func(t *testing.T) {
		svc := &armsearch.Service{ID: &svcID}
		edges := searchEdges(svc)
		assert.Empty(t, edges)
	})
}
