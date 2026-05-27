// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestEHNamespaceEncryptionEdges(t *testing.T) {
	nsID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/my-ns"
	kvURI := "https://myvault.vault.azure.net/"
	keyName := "ehkey"

	t.Run("emits ENCRYPTS_WITH when CMK configured", func(t *testing.T) {
		ns := &armeventhub.EHNamespace{
			ID: &nsID,
			Properties: &armeventhub.EHNamespaceProperties{
				Encryption: &armeventhub.Encryption{
					KeyVaultProperties: []*armeventhub.KeyVaultProperties{{
						KeyVaultURI: &kvURI,
						KeyName:     &keyName,
					}},
				},
			},
		}
		edges := ehNamespaceEncryptionEdges(ns)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
		assert.Equal(t, nsID, edges[0].SourceID)
		assert.Equal(t, "https://myvault.vault.azure.net/keys/ehkey", edges[0].TargetID)
	})

	t.Run("no edge when no encryption", func(t *testing.T) {
		ns := &armeventhub.EHNamespace{
			ID:         &nsID,
			Properties: &armeventhub.EHNamespaceProperties{},
		}
		edges := ehNamespaceEncryptionEdges(ns)
		assert.Empty(t, edges)
	})
}

func TestEHCaptureEdge(t *testing.T) {
	ehID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/ns/eventhubs/eh1"
	storageID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/mystorage"

	t.Run("emits SINKS_TO when capture enabled", func(t *testing.T) {
		enabled := true
		eh := &armeventhub.Eventhub{
			ID: &ehID,
			Properties: &armeventhub.Properties{
				CaptureDescription: &armeventhub.CaptureDescription{
					Enabled: &enabled,
					Destination: &armeventhub.Destination{
						Properties: &armeventhub.DestinationProperties{
							StorageAccountResourceID: &storageID,
						},
					},
				},
			},
		}
		edges := ehCaptureEdge(eh)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeSinksTo, edges[0].Relationship)
		assert.Equal(t, ehID, edges[0].SourceID)
		assert.Equal(t, storageID, edges[0].TargetID)
	})

	t.Run("no edge when capture disabled", func(t *testing.T) {
		disabled := false
		eh := &armeventhub.Eventhub{
			ID: &ehID,
			Properties: &armeventhub.Properties{
				CaptureDescription: &armeventhub.CaptureDescription{
					Enabled: &disabled,
				},
			},
		}
		edges := ehCaptureEdge(eh)
		assert.Empty(t, edges)
	})

	t.Run("no edge when no capture", func(t *testing.T) {
		eh := &armeventhub.Eventhub{
			ID:         &ehID,
			Properties: &armeventhub.Properties{},
		}
		edges := ehCaptureEdge(eh)
		assert.Empty(t, edges)
	})
}
