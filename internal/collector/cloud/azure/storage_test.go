// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestStorageEdges_KeyVault(t *testing.T) {
	accountID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/mystg"
	kvURI := "https://myvault.vault.azure.net/"
	keyName := "mykey"
	keyVersion := "v1"

	t.Run("emits EdgeEncryptsWith when CMK configured", func(t *testing.T) {
		account := &armstorage.Account{
			ID: &accountID,
			Properties: &armstorage.AccountProperties{
				Encryption: &armstorage.Encryption{
					KeyVaultProperties: &armstorage.KeyVaultProperties{
						KeyVaultURI: &kvURI,
						KeyName:     &keyName,
						KeyVersion:  &keyVersion,
					},
				},
			},
		}
		edges := storageEdges(account)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, accountID, e.SourceID)
				assert.Equal(t, "https://myvault.vault.azure.net/keys/mykey/v1", e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when no encryption config", func(t *testing.T) {
		account := &armstorage.Account{
			ID:         &accountID,
			Properties: &armstorage.AccountProperties{},
		}
		edges := storageEdges(account)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}
