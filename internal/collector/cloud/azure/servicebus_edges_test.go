// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSBQueueDeadLetterEdge(t *testing.T) {
	queueID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/queues/my-queue"
	dlqName := "dead-letter-queue"

	t.Run("emits DEAD_LETTERS_TO when ForwardDeadLetteredMessagesTo set", func(t *testing.T) {
		q := &armservicebus.SBQueue{
			ID: &queueID,
			Properties: &armservicebus.SBQueueProperties{
				ForwardDeadLetteredMessagesTo: &dlqName,
			},
		}
		edges := sbQueueDeadLetterEdge(q)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeDeadLettersTo, edges[0].Relationship)
		assert.Equal(t, queueID, edges[0].SourceID)
		// Should resolve to sibling queue under same namespace.
		assert.Contains(t, edges[0].TargetID, dlqName)
	})

	t.Run("emits DEAD_LETTERS_TO to implicit DLQ on expiration", func(t *testing.T) {
		enabled := true
		q := &armservicebus.SBQueue{
			ID: &queueID,
			Properties: &armservicebus.SBQueueProperties{
				DeadLetteringOnMessageExpiration: &enabled,
			},
		}
		edges := sbQueueDeadLetterEdge(q)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeDeadLettersTo, edges[0].Relationship)
		assert.Equal(t, queueID+"/$deadletterqueue", edges[0].TargetID)
	})

	t.Run("no edge when no dead letter config", func(t *testing.T) {
		q := &armservicebus.SBQueue{
			ID:         &queueID,
			Properties: &armservicebus.SBQueueProperties{},
		}
		edges := sbQueueDeadLetterEdge(q)
		assert.Empty(t, edges)
	})
}

func TestSBNamespaceEncryptionEdges(t *testing.T) {
	nsID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/my-ns"
	kvURI := "https://myvault.vault.azure.net/"
	keyName := "mykey"
	keyVer := "v1"

	t.Run("emits ENCRYPTS_WITH when CMK configured", func(t *testing.T) {
		ns := &armservicebus.SBNamespace{
			ID: &nsID,
			Properties: &armservicebus.SBNamespaceProperties{
				Encryption: &armservicebus.Encryption{
					KeyVaultProperties: []*armservicebus.KeyVaultProperties{{
						KeyVaultURI: &kvURI,
						KeyName:     &keyName,
						KeyVersion:  &keyVer,
					}},
				},
			},
		}
		edges := sbNamespaceEncryptionEdges(ns)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
		assert.Equal(t, nsID, edges[0].SourceID)
		assert.Equal(t, "https://myvault.vault.azure.net/keys/mykey/v1", edges[0].TargetID)
	})

	t.Run("no edge when no encryption", func(t *testing.T) {
		ns := &armservicebus.SBNamespace{
			ID:         &nsID,
			Properties: &armservicebus.SBNamespaceProperties{},
		}
		edges := sbNamespaceEncryptionEdges(ns)
		assert.Empty(t, edges)
	})
}

func TestSBSubscriptionDeadLetterEdge(t *testing.T) {
	subID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/topics/t1/subscriptions/s1"
	dlqName := "dlq-queue"

	t.Run("emits DEAD_LETTERS_TO when forwarding set", func(t *testing.T) {
		sub := &armservicebus.SBSubscription{
			ID: &subID,
			Properties: &armservicebus.SBSubscriptionProperties{
				ForwardDeadLetteredMessagesTo: &dlqName,
			},
		}
		edges := sbSubscriptionDeadLetterEdge(sub)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeDeadLettersTo, edges[0].Relationship)
	})

	t.Run("emits implicit DLQ on expiration", func(t *testing.T) {
		enabled := true
		sub := &armservicebus.SBSubscription{
			ID: &subID,
			Properties: &armservicebus.SBSubscriptionProperties{
				DeadLetteringOnMessageExpiration: &enabled,
			},
		}
		edges := sbSubscriptionDeadLetterEdge(sub)
		require.Len(t, edges, 1)
		assert.Equal(t, subID+"/$deadletterqueue", edges[0].TargetID)
	})
}
