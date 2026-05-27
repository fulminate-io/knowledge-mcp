// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"cloud.google.com/go/iam"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	gcs "cloud.google.com/go/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- EdgeEncryptsWith (CMEK) ---

func TestStorageBucketEdges_WithCMEK(t *testing.T) {
	kmsKey := "projects/my-proj/locations/us/keyRings/ring/cryptoKeys/key"
	bucket := &gcs.BucketAttrs{
		Name:       "my-bucket",
		Encryption: &gcs.BucketEncryption{DefaultKMSKeyName: kmsKey},
	}
	edges := storageBucketEdges("gs://my-bucket", bucket)
	require.Len(t, edges, 1)
	assert.Equal(t, "gs://my-bucket", edges[0].SourceID)
	assert.Equal(t, kmsKey, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
}

func TestStorageBucketEdges_NilEncryption(t *testing.T) {
	bucket := &gcs.BucketAttrs{Name: "plain-bucket"}
	edges := storageBucketEdges("gs://plain-bucket", bucket)
	assert.Empty(t, edges)
}

func TestStorageBucketEdges_EmptyKMSKey(t *testing.T) {
	bucket := &gcs.BucketAttrs{
		Name:       "empty-key",
		Encryption: &gcs.BucketEncryption{DefaultKMSKeyName: ""},
	}
	edges := storageBucketEdges("gs://empty-key", bucket)
	assert.Empty(t, edges)
}

// --- EdgeSinksTo (log bucket) ---

func TestStorageBucketEdges_WithLogging(t *testing.T) {
	bucket := &gcs.BucketAttrs{
		Name: "source-bucket",
		Logging: &gcs.BucketLogging{
			LogBucket:       "logs-bucket",
			LogObjectPrefix: "gcs-logs/",
		},
	}
	edges := storageBucketEdges("gs://source-bucket", bucket)
	require.Len(t, edges, 1)
	assert.Equal(t, "gs://source-bucket", edges[0].SourceID)
	assert.Equal(t, "gs://logs-bucket", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeSinksTo, edges[0].Relationship)
	assert.Equal(t, "gcs-logs/", edges[0].Metadata["log_object_prefix"])
}

func TestStorageBucketEdges_CMEKAndLogging(t *testing.T) {
	kmsKey := "projects/p/locations/us/keyRings/r/cryptoKeys/k"
	bucket := &gcs.BucketAttrs{
		Name:       "both",
		Encryption: &gcs.BucketEncryption{DefaultKMSKeyName: kmsKey},
		Logging:    &gcs.BucketLogging{LogBucket: "log-dest"},
	}
	edges := storageBucketEdges("gs://both", bucket)
	require.Len(t, edges, 2)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeSinksTo, edges[1].Relationship)
}

// --- EdgeGrants (IAM) ---

func TestStorageBucketGrantsEdges(t *testing.T) {
	policy := &iam.Policy{
		InternalProto: &iampb.Policy{
			Bindings: []*iampb.Binding{
				{
					Role:    "roles/storage.admin",
					Members: []string{"serviceAccount:sa@proj.iam.gserviceaccount.com"},
				},
				{
					Role:    "roles/storage.viewer",
					Members: []string{"user:alice@example.com", "user:bob@example.com"},
				},
			},
		},
	}
	edges := storageBucketGrantsEdges("gs://my-bucket", policy)
	require.Len(t, edges, 3)

	// Sorted by role then member.
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, "gs://my-bucket", edges[0].SourceID)
	assert.Equal(t, "serviceAccount:sa@proj.iam.gserviceaccount.com", edges[0].TargetID)
	assert.Equal(t, "roles/storage.admin", edges[0].Metadata["role"])

	assert.Equal(t, "user:alice@example.com", edges[1].TargetID)
	assert.Equal(t, "roles/storage.viewer", edges[1].Metadata["role"])

	assert.Equal(t, "user:bob@example.com", edges[2].TargetID)
}

func TestStorageBucketGrantsEdges_NilPolicy(t *testing.T) {
	edges := storageBucketGrantsEdges("gs://b", nil)
	assert.Empty(t, edges)
}

func TestStorageBucketGrantsEdges_EmptyBindings(t *testing.T) {
	policy := &iam.Policy{InternalProto: &iampb.Policy{}}
	edges := storageBucketGrantsEdges("gs://b", policy)
	assert.Empty(t, edges)
}

// --- EdgeTriggers (Pub/Sub notifications) ---

func TestStorageBucketNotifyEdges(t *testing.T) {
	notifs := map[string]*gcs.Notification{
		"n1": {
			ID:             "n1",
			TopicProjectID: "my-proj",
			TopicID:        "my-topic",
			EventTypes:     []string{"OBJECT_FINALIZE"},
			PayloadFormat:  "JSON_API_V1",
		},
	}
	seen := map[string]bool{}
	edges, proxies := storageBucketNotifyEdges("gs://b", notifs, seen)
	require.Len(t, edges, 1)
	assert.Equal(t, "gs://b", edges[0].SourceID)
	assert.Equal(t, "projects/my-proj/topics/my-topic", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, "OBJECT_FINALIZE", edges[0].Metadata["event_types"])
	assert.Equal(t, "JSON_API_V1", edges[0].Metadata["payload_format"])

	// Proxy emitted for uncollected topic.
	require.Len(t, proxies, 1)
	assert.Equal(t, "gcp:pubsub:topic", proxies[0].ResourceType)
	assert.Equal(t, "false", proxies[0].Metadata["collected"])
}

func TestStorageBucketNotifyEdges_DedupeProxy(t *testing.T) {
	notifs := map[string]*gcs.Notification{
		"n1": {
			ID: "n1", TopicProjectID: "p", TopicID: "t",
			PayloadFormat: "JSON_API_V1",
		},
	}
	seen := map[string]bool{"projects/p/topics/t": true}
	_, proxies := storageBucketNotifyEdges("gs://b", notifs, seen)
	assert.Empty(t, proxies, "proxy should not be emitted when already seen")
}

func TestStorageBucketNotifyEdges_Empty(t *testing.T) {
	edges, proxies := storageBucketNotifyEdges("gs://b", nil, nil)
	assert.Empty(t, edges)
	assert.Empty(t, proxies)
}
