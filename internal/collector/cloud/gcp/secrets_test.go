// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSecretsSubCollector_Name(t *testing.T) {
	c := &secretsSubCollector{}
	assert.Equal(t, "gcp-secrets", c.Name())
}

func TestSecretCMEKEdges_AutomaticReplication(t *testing.T) {
	kmsKey := "projects/p/locations/us/keyRings/ring/cryptoKeys/key"
	secret := &secretmanagerpb.Secret{
		Name: "projects/p/secrets/my-secret",
		Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{
				Automatic: &secretmanagerpb.Replication_Automatic{
					CustomerManagedEncryption: &secretmanagerpb.CustomerManagedEncryption{
						KmsKeyName: kmsKey,
					},
				},
			},
		},
	}

	edges := secretCMEKEdges("projects/p/secrets/my-secret", secret)
	require.Len(t, edges, 1)
	assert.Equal(t, "projects/p/secrets/my-secret", edges[0].SourceID)
	assert.Equal(t, kmsKey, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
}

func TestSecretCMEKEdges_UserManagedReplicas(t *testing.T) {
	key1 := "projects/p/locations/us/keyRings/ring/cryptoKeys/key1"
	key2 := "projects/p/locations/eu/keyRings/ring/cryptoKeys/key2"
	secret := &secretmanagerpb.Secret{
		Name: "projects/p/secrets/multi-region",
		Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_UserManaged_{
				UserManaged: &secretmanagerpb.Replication_UserManaged{
					Replicas: []*secretmanagerpb.Replication_UserManaged_Replica{
						{
							Location: "us-central1",
							CustomerManagedEncryption: &secretmanagerpb.CustomerManagedEncryption{
								KmsKeyName: key1,
							},
						},
						{
							Location: "europe-west1",
							CustomerManagedEncryption: &secretmanagerpb.CustomerManagedEncryption{
								KmsKeyName: key2,
							},
						},
					},
				},
			},
		},
	}

	edges := secretCMEKEdges("projects/p/secrets/multi-region", secret)
	require.Len(t, edges, 2)
	assert.Equal(t, key1, edges[0].TargetID)
	assert.Equal(t, key2, edges[1].TargetID)
}

func TestSecretCMEKEdges_NoCMEK(t *testing.T) {
	secret := &secretmanagerpb.Secret{
		Name: "projects/p/secrets/plain",
		Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{
				Automatic: &secretmanagerpb.Replication_Automatic{},
			},
		},
	}

	edges := secretCMEKEdges("projects/p/secrets/plain", secret)
	assert.Empty(t, edges)
}

func TestSecretCMEKEdges_NilReplication(t *testing.T) {
	secret := &secretmanagerpb.Secret{Name: "projects/p/secrets/no-repl"}
	edges := secretCMEKEdges("projects/p/secrets/no-repl", secret)
	assert.Empty(t, edges)
}
