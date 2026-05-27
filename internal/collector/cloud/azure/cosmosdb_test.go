// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	armcosmos "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v3"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCosmosEdges_KeyVault(t *testing.T) {
	accountID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.DocumentDB/databaseAccounts/mydb"
	kvKeyURI := "https://myvault.vault.azure.net/keys/cosmos-key/v1"

	t.Run("emits EdgeEncryptsWith when KeyVaultKeyUri set", func(t *testing.T) {
		account := &armcosmos.DatabaseAccountGetResults{
			ID: &accountID,
			Properties: &armcosmos.DatabaseAccountGetProperties{
				KeyVaultKeyURI: &kvKeyURI,
			},
		}
		edges := cosmosEdges(account)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, accountID, e.SourceID)
				assert.Equal(t, kvKeyURI, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when KeyVaultKeyUri empty", func(t *testing.T) {
		account := &armcosmos.DatabaseAccountGetResults{
			ID:         &accountID,
			Properties: &armcosmos.DatabaseAccountGetProperties{},
		}
		edges := cosmosEdges(account)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}
