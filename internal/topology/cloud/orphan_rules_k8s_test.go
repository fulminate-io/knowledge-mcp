// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// orphan_rules_k8s_test.go covers the eight v1 K8s orphan rules. Each
// rule has a positive (orphan) and negative (referenced) test. The Pod
// rule additionally has a static-pod test that exercises the
// annotation/kubernetes.io/config.source skip path.

const k8sAcct = "test-cluster"

// TestK8sRulesRegistered asserts all eight v1 K8s rules are present in
// the dispatch table.
func TestK8sRulesRegistered(t *testing.T) {
	expected := []string{
		"Deployment", "StatefulSet", "DaemonSet",
		"Pod", "Service",
		"PersistentVolume", "ConfigMap", "Secret",
	}
	for _, rt := range expected {
		_, ok := lookupOrphanRule(rt)
		assert.Truef(t, ok, "expected orphan rule registered for %q", rt)
	}
}

// --- Workload controllers (Deployment / StatefulSet / DaemonSet) ---

func TestWorkloadControllerRule_Orphan(t *testing.T) {
	cases := []string{"Deployment", "StatefulSet", "DaemonSet"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			fx := newCloudFixture(t)
			id := "default/" + kind + "/web"
			fx.AddCloudResource(k8sAcct, id, "web", kind, nil)

			rule := workloadControllerRule(kind)
			orphan, conf, summary, err := rule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, id))
			require.NoError(t, err)
			assert.True(t, orphan)
			assert.InDelta(t, 0.8, conf, 0.0001)
			assert.Contains(t, summary, kind)
		})
	}
}

func TestWorkloadControllerRule_HasPods_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Deployment/web", "web", "Deployment", nil)
	fx.AddCloudResource(k8sAcct, "default/Pod/web-abc", "web-abc", "Pod", nil)
	// Pod → controller via OWNED_BY (matches cloud/k8s/sub_pods.go semantics).
	fx.AddEdge(k8sAcct, "default/Pod/web-abc", "default/Deployment/web", kgtypes.EdgeOwnedBy)

	rule := workloadControllerRule("Deployment")
	orphan, _, _, err := rule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Deployment/web"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- Pod ---

func TestPodRule_Orphan_NoOwner(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Pod/bare", "bare", "Pod", nil)

	orphan, conf, _, err := podRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Pod/bare"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
}

func TestPodRule_Owned_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Pod/web-abc", "web-abc", "Pod", nil)
	fx.AddCloudResource(k8sAcct, "default/ReplicaSet/web", "web", "ReplicaSet", nil)
	fx.AddEdge(k8sAcct, "default/Pod/web-abc", "default/ReplicaSet/web", kgtypes.EdgeOwnedBy)

	orphan, _, _, err := podRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Pod/web-abc"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// TestPodRule_StaticPod_NotOrphan exercises the static-pod skip: a pod with
// no owner reference but the kubernetes.io/config.source=file annotation
// is a static pod (kubelet-managed) and must NOT be flagged.
func TestPodRule_StaticPod_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "kube-system/Pod/kube-apiserver-node1", "kube-apiserver-node1", "Pod",
		map[string]string{"annotation/kubernetes.io/config.source": "file"})

	orphan, _, _, err := podRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "kube-system/Pod/kube-apiserver-node1"))
	require.NoError(t, err)
	assert.False(t, orphan, "static pod (annotation/kubernetes.io/config.source=file) must not be flagged as orphan")
}

// --- Service ---

func TestServiceRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Service/web", "web", "Service", nil)

	orphan, conf, _, err := serviceRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Service/web"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
}

func TestServiceRule_SelectsPods_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Service/web", "web", "Service", nil)
	fx.AddCloudResource(k8sAcct, "default/Pod/web-abc", "web-abc", "Pod", nil)
	fx.AddEdge(k8sAcct, "default/Service/web", "default/Pod/web-abc", kgtypes.EdgeSelects)

	orphan, _, _, err := serviceRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Service/web"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- PersistentVolume ---

func TestPersistentVolumeRule_Orphan_PhaseAvailable(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "PersistentVolume/pv-1", "pv-1", "PersistentVolume",
		map[string]string{"phase": "Available"})

	orphan, conf, summary, err := persistentVolumeRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "PersistentVolume/pv-1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
	assert.Contains(t, summary, "Available")
}

func TestPersistentVolumeRule_Orphan_PhaseBound_LowerConfidence(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "PersistentVolume/pv-2", "pv-2", "PersistentVolume",
		map[string]string{"phase": "Bound"})

	orphan, conf, _, err := persistentVolumeRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "PersistentVolume/pv-2"))
	require.NoError(t, err)
	assert.True(t, orphan, "PV with phase=Bound but no inbound BOUND_TO is still flagged, just with lower confidence")
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestPersistentVolumeRule_BoundByPVC_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "PersistentVolume/pv-3", "pv-3", "PersistentVolume",
		map[string]string{"phase": "Bound"})
	fx.AddCloudResource(k8sAcct, "default/PersistentVolumeClaim/data", "data", "PersistentVolumeClaim", nil)
	fx.AddEdge(k8sAcct, "default/PersistentVolumeClaim/data", "PersistentVolume/pv-3", kgtypes.EdgeBoundTo)

	orphan, _, _, err := persistentVolumeRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "PersistentVolume/pv-3"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- ConfigMap ---

func TestConfigMapRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/ConfigMap/cfg", "cfg", "ConfigMap", nil)

	orphan, conf, _, err := configMapRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/ConfigMap/cfg"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestConfigMapRule_Mounted_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/ConfigMap/cfg", "cfg", "ConfigMap", nil)
	fx.AddCloudResource(k8sAcct, "default/Deployment/web", "web", "Deployment", nil)
	fx.AddEdge(k8sAcct, "default/Deployment/web", "default/ConfigMap/cfg", kgtypes.EdgeMountsConfigMap)

	orphan, _, _, err := configMapRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/ConfigMap/cfg"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- Secret ---

func TestSecretRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Secret/db-creds", "db-creds", "Secret", nil)

	orphan, conf, _, err := secretRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Secret/db-creds"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestSecretRule_Mounted_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(k8sAcct, "default/Secret/db-creds", "db-creds", "Secret", nil)
	fx.AddCloudResource(k8sAcct, "default/Deployment/api", "api", "Deployment", nil)
	fx.AddEdge(k8sAcct, "default/Deployment/api", "default/Secret/db-creds", kgtypes.EdgeMountsSecret)

	orphan, _, _, err := secretRule(context.Background(), fx, k8sAcct, fx.orphanGraphFor(t, k8sAcct), fx.nodeFor(t, k8sAcct, "default/Secret/db-creds"))
	require.NoError(t, err)
	assert.False(t, orphan)
}
