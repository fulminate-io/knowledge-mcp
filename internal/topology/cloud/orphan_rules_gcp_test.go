// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const gcpAcct = "my-project"

// TestGCPRulesRegistered asserts all GCP orphan rules are present.
func TestGCPRulesRegistered(t *testing.T) {
	for _, rt := range []string{
		"gcp:compute:forwardingRule",
		"gcp:compute:backendService",
		"gcp:storage:bucket",
		"gcp:compute:disk",
		"gcp:firestore:database",
		"gcp:artifactregistry:repository",
		"gcp:cloudkms:cryptoKey",
		"gcp:compute:router",
		"gcp:compute:sslCertificate",
		"gcp:monitoring:alertPolicy",
	} {
		_, ok := lookupOrphanRule(rt)
		assert.Truef(t, ok, "expected orphan rule registered for %q", rt)
	}
}

// --- gcp:compute:forwardingRule ---

func TestGCPForwardingRuleRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/forwardingRules/lb-1"
	fx.AddCloudResource(gcpAcct, id, "lb-1", "gcp:compute:forwardingRule", nil)

	orphan, conf, _, err := gcpForwardingRuleRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
}

func TestGCPForwardingRuleRule_HasTarget_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/forwardingRules/lb-2"
	fx.AddCloudResource(gcpAcct, id, "lb-2", "gcp:compute:forwardingRule", nil)
	fx.AddCloudResource(gcpAcct, "projects/my-project/global/targetHttpProxies/proxy-2", "proxy-2", "gcp:compute:targetHttpProxy", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/global/targetHttpProxies/proxy-2", kgtypes.EdgeTargets)

	orphan, _, _, err := gcpForwardingRuleRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:compute:backendService ---

func TestGCPBackendServiceRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/backendServices/bs-1"
	fx.AddCloudResource(gcpAcct, id, "bs-1", "gcp:compute:backendService", nil)

	orphan, conf, _, err := gcpBackendServiceRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestGCPBackendServiceRule_HasBackends_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/backendServices/bs-2"
	fx.AddCloudResource(gcpAcct, id, "bs-2", "gcp:compute:backendService", nil)
	fx.AddCloudResource(gcpAcct, "projects/my-project/zones/us-central1-a/instanceGroups/ig-2", "ig-2", "gcp:compute:instanceGroup", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/zones/us-central1-a/instanceGroups/ig-2", kgtypes.EdgeTargets)

	orphan, _, _, err := gcpBackendServiceRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:storage:bucket ---

func TestGCPStorageBucketRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(gcpAcct, "gs://lonely-bucket", "lonely-bucket", "gcp:storage:bucket", nil)

	orphan, conf, msg, err := gcpStorageBucketRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, "gs://lonely-bucket"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
	assert.Contains(t, msg, "lonely-bucket")
}

func TestGCPStorageBucketRule_HasGrants_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(gcpAcct, "gs://granted-bucket", "granted-bucket", "gcp:storage:bucket", nil)
	fx.AddEdge(gcpAcct, "gs://granted-bucket", "user:alice@example.com", kgtypes.EdgeGrants)

	orphan, _, _, err := gcpStorageBucketRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, "gs://granted-bucket"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestGCPStorageBucketRule_HasTriggers_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(gcpAcct, "gs://notified-bucket", "notified-bucket", "gcp:storage:bucket", nil)
	fx.AddEdge(gcpAcct, "gs://notified-bucket", "projects/p/topics/t", kgtypes.EdgeTriggers)

	orphan, _, _, err := gcpStorageBucketRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, "gs://notified-bucket"))
	require.NoError(t, err)
	assert.False(t, orphan)
}
