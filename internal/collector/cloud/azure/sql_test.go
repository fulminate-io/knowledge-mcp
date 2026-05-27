// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSQLServerEdges_KeyVault(t *testing.T) {
	// Use a short ID that won't parse for rg/serverName, so the PE client
	// (nil in tests) is never called.
	serverID := "/simple-server"
	keyURI := "https://myvault.vault.azure.net/keys/mykey/version123"

	t.Run("emits EdgeEncryptsWith when KeyID set", func(t *testing.T) {
		server := &armsql.Server{
			ID:   &serverID,
			Name: new("myserver"),
			Properties: &armsql.ServerProperties{
				KeyID: &keyURI,
			},
		}
		edges := sqlServerEdges(context.TODO(), server, nil)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, serverID, e.SourceID)
				assert.Equal(t, keyURI, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when KeyID empty", func(t *testing.T) {
		server := &armsql.Server{
			ID:         &serverID,
			Name:       new("myserver"),
			Properties: &armsql.ServerProperties{},
		}
		edges := sqlServerEdges(context.TODO(), server, nil)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}
