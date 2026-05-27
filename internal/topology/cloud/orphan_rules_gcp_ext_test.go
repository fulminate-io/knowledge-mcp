// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- gcp:compute:disk ---

func TestGCPDiskRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/zones/us-central1-a/disks/orphan-disk"
	fx.AddCloudResource(gcpAcct, id, "orphan-disk", "gcp:compute:disk", nil)

	orphan, conf, msg, err := gcpDiskRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
	assert.Contains(t, msg, "orphan-disk")
}

func TestGCPDiskRule_HasBoundTo_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/zones/us-central1-a/disks/attached-disk"
	fx.AddCloudResource(gcpAcct, id, "attached-disk", "gcp:compute:disk", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/zones/us-central1-a/instances/vm-1", kgtypes.EdgeBoundTo)

	orphan, _, _, err := gcpDiskRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestGCPDiskRule_HasFromSnapshot_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/zones/us-central1-a/disks/snapped-disk"
	fx.AddCloudResource(gcpAcct, id, "snapped-disk", "gcp:compute:disk", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/global/snapshots/snap-1", kgtypes.EdgeFromSnapshot)

	orphan, _, _, err := gcpDiskRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:firestore:database ---

func TestGCPFirestoreDBRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/databases/(default)"
	fx.AddCloudResource(gcpAcct, id, "(default)", "gcp:firestore:database", nil)

	orphan, conf, msg, err := gcpFirestoreDBRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
	assert.Contains(t, msg, "(default)")
}

func TestGCPFirestoreDBRule_HasGrants_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/databases/(default)"
	fx.AddCloudResource(gcpAcct, id, "(default)", "gcp:firestore:database", nil)
	fx.AddEdge(gcpAcct, id, "user:admin@example.com", kgtypes.EdgeGrants)

	orphan, _, _, err := gcpFirestoreDBRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestGCPFirestoreDBRule_HasBackup_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/databases/(default)"
	fx.AddCloudResource(gcpAcct, id, "(default)", "gcp:firestore:database", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/databases/(default)/backupSchedules/daily-1", kgtypes.EdgeBackedUpBy)

	orphan, _, _, err := gcpFirestoreDBRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:artifactregistry:repository ---

func TestGCPARRepoRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/locations/us/repositories/empty-repo"
	fx.AddCloudResource(gcpAcct, id, "empty-repo", "gcp:artifactregistry:repository", nil)

	orphan, conf, _, err := gcpARRepoRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestGCPARRepoRule_HasGrants_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/locations/us/repositories/granted-repo"
	fx.AddCloudResource(gcpAcct, id, "granted-repo", "gcp:artifactregistry:repository", nil)
	fx.AddEdge(gcpAcct, id, "user:alice@example.com", kgtypes.EdgeGrants)

	orphan, _, _, err := gcpARRepoRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:cloudkms:cryptoKey ---

func TestGCPKMSKeyRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/locations/us/keyRings/r/cryptoKeys/orphan-key"
	fx.AddCloudResource(gcpAcct, id, "orphan-key", "gcp:cloudkms:cryptoKey", nil)

	orphan, conf, _, err := gcpKMSKeyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestGCPKMSKeyRule_HasGrant_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/locations/us/keyRings/r/cryptoKeys/granted-key"
	fx.AddCloudResource(gcpAcct, id, "granted-key", "gcp:cloudkms:cryptoKey", nil)
	fx.AddEdge(gcpAcct, id, "serviceAccount:sa@proj.iam.gserviceaccount.com", kgtypes.EdgeGrants)

	orphan, _, _, err := gcpKMSKeyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestGCPKMSKeyRule_HasEncryptionRef_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/locations/us/keyRings/r/cryptoKeys/used-key"
	fx.AddCloudResource(gcpAcct, id, "used-key", "gcp:cloudkms:cryptoKey", nil)
	fx.AddCloudResource(gcpAcct, "gs://encrypted-bucket", "encrypted-bucket", "gcp:storage:bucket", nil)
	fx.AddEdge(gcpAcct, "gs://encrypted-bucket", id, kgtypes.EdgeEncryptsWith)

	orphan, _, _, err := gcpKMSKeyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:compute:router ---

func TestGCPRouterRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/regions/us-central1/routers/orphan-router"
	fx.AddCloudResource(gcpAcct, id, "orphan-router", "gcp:compute:router", nil)

	orphan, conf, _, err := gcpRouterRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestGCPRouterRule_HasNetwork_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/regions/us-central1/routers/connected-router"
	fx.AddCloudResource(gcpAcct, id, "connected-router", "gcp:compute:router", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/global/networks/my-vpc", kgtypes.EdgeUsesNetwork)

	orphan, _, _, err := gcpRouterRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:compute:sslCertificate ---

func TestGCPSSLCertRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/sslCertificates/unused-cert"
	fx.AddCloudResource(gcpAcct, id, "unused-cert", "gcp:compute:sslCertificate", nil)

	orphan, conf, _, err := gcpSSLCertRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestGCPSSLCertRule_HasUsesCert_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/global/sslCertificates/active-cert"
	fx.AddCloudResource(gcpAcct, id, "active-cert", "gcp:compute:sslCertificate", nil)
	fx.AddCloudResource(gcpAcct, "projects/my-project/global/targetHttpsProxies/proxy-1", "proxy-1", "gcp:compute:targetHttpsProxy", nil)
	fx.AddEdge(gcpAcct, "projects/my-project/global/targetHttpsProxies/proxy-1", id, kgtypes.EdgeUsesCert)

	orphan, _, _, err := gcpSSLCertRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- gcp:monitoring:alertPolicy ---

func TestGCPAlertPolicyRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/alertPolicies/dead-policy"
	fx.AddCloudResource(gcpAcct, id, "dead-policy", "gcp:monitoring:alertPolicy", nil)

	orphan, conf, _, err := gcpAlertPolicyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestGCPAlertPolicyRule_HasMonitors_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/alertPolicies/active-policy"
	fx.AddCloudResource(gcpAcct, id, "active-policy", "gcp:monitoring:alertPolicy", nil)
	fx.AddEdge(gcpAcct, id, "gs://my-bucket", kgtypes.EdgeMonitors)

	orphan, _, _, err := gcpAlertPolicyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestGCPAlertPolicyRule_HasNotifiesVia_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	id := "projects/my-project/alertPolicies/notified-policy"
	fx.AddCloudResource(gcpAcct, id, "notified-policy", "gcp:monitoring:alertPolicy", nil)
	fx.AddEdge(gcpAcct, id, "projects/my-project/notificationChannels/ch-1", kgtypes.EdgeNotifiesVia)

	orphan, _, _, err := gcpAlertPolicyRule(context.Background(), fx, gcpAcct, fx.orphanGraphFor(t, gcpAcct), fx.nodeFor(t, gcpAcct, id))
	require.NoError(t, err)
	assert.False(t, orphan)
}
