// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPodsSubCollector_AnnotationsPreserved verifies that pod annotations
// are copied into the resource node's metadata under the "annotation/<key>"
// prefix. This is the contract that downstream analyzers (specifically the
// topology orphan-detection podRule) rely on to distinguish static pods
// (annotation kubernetes.io/config.source=file) from genuinely orphaned
// pods that were created bare without an owner reference.
//
// The "annotation/" prefix mirrors the existing "label/" scheme produced
// by labelsToMeta — see helpers.go for the symmetric label preservation.
func TestPodsSubCollector_AnnotationsPreserved(t *testing.T) {
	staticPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-apiserver-node1",
			Namespace: "kube-system",
			Labels:    map[string]string{"component": "kube-apiserver"},
			Annotations: map[string]string{
				"kubernetes.io/config.source":                      "file",
				"kubernetes.io/config.hash":                        "abc123",
				"kubectl.kubernetes.io/last-applied-configuration": "{}",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node1",
			Containers: []corev1.Container{
				{Name: "apiserver", Image: "k8s.gcr.io/kube-apiserver:v1.28.0"},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	orphanPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "bare-pod",
			Namespace:   "default",
			Labels:      map[string]string{"app": "bare"},
			Annotations: nil, // no annotations at all
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cs := fake.NewSimpleClientset(staticPod, orphanPod)
	sub := &podsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)

	// Index by ID for stable lookup regardless of fake-clientset list order.
	byID := make(map[string]cloud.ResourceSpec, 2)
	for _, r := range result.Resources {
		byID[r.ID] = r
	}

	staticID := "kube-system/Pod/kube-apiserver-node1"
	require.Contains(t, byID, staticID, "static pod resource missing from collector output")
	staticMeta := byID[staticID].Metadata
	assert.Equal(t, "file", staticMeta["annotation/kubernetes.io/config.source"],
		"static pod must preserve kubernetes.io/config.source annotation under annotation/<key>")
	assert.Equal(t, "abc123", staticMeta["annotation/kubernetes.io/config.hash"],
		"static pod must preserve every annotation, not just the static-pod marker")
	assert.Equal(t, "kube-apiserver", staticMeta["label/component"],
		"existing label preservation must continue to work alongside annotation preservation")

	orphanID := "default/Pod/bare-pod"
	require.Contains(t, byID, orphanID, "orphan pod resource missing from collector output")
	orphanMeta := byID[orphanID].Metadata
	for k := range orphanMeta {
		assert.NotContains(t, k, "annotation/",
			"pod with no annotations must not gain spurious annotation/* keys; got %s=%q", k, orphanMeta[k])
	}

	// Sanity: verify the OWNED_BY edge contract is unchanged. A pod with no
	// owner references must produce zero EdgeOwnedBy edges — this is what
	// the orphan rule's "no inbound owner" check relies on.
	for _, e := range result.Edges {
		if e.SourceID == orphanID && e.Relationship == kgtypes.EdgeOwnedBy {
			t.Fatalf("orphan pod must not have any OWNED_BY edges; found %+v", e)
		}
	}
}

// TestPodsSubCollector_RunsOnEdge verifies the EdgeRunsOn contract added
// in Phase 3: every scheduled pod (Spec.NodeName != "") emits exactly
// one RUNS_ON edge to Node/<nodeName>, and unscheduled pods emit none.
// Static pods (kubernetes.io/config.source=file) participate uniformly
// per the plan's open-question resolution — the Node resource exists
// regardless of how the pod was created, and filtering static vs
// regular is a query-time concern against the preserved annotation
// metadata.
func TestPodsSubCollector_RunsOnEdge(t *testing.T) {
	scheduledPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:   "gke-main-default-pool-abc",
			Containers: []corev1.Container{{Name: "web", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	staticPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-apiserver-control-1",
			Namespace: "kube-system",
			Annotations: map[string]string{
				"kubernetes.io/config.source": "file",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   "control-1",
			Containers: []corev1.Container{{Name: "apiserver", Image: "k8s.gcr.io/kube-apiserver:v1.28.0"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	unscheduledPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-abc",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			// NodeName intentionally empty — pod is still being scheduled.
			Containers: []corev1.Container{{Name: "main", Image: "alpine:3"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	cs := fake.NewSimpleClientset(scheduledPod, staticPod, unscheduledPod)
	sub := &podsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 3)

	// Index every RUNS_ON edge by source pod ID so assertions stay stable
	// regardless of iteration order.
	runsOnBySource := map[string]string{}
	for _, e := range result.Edges {
		if e.Relationship != kgtypes.EdgeRunsOn {
			continue
		}
		_, dup := runsOnBySource[e.SourceID]
		assert.False(t, dup, "pod %s must emit at most one RUNS_ON edge", e.SourceID)
		runsOnBySource[e.SourceID] = e.TargetID
	}

	const (
		scheduledID    = "default/Pod/web-0"
		staticID       = "kube-system/Pod/kube-apiserver-control-1"
		unscheduledID  = "default/Pod/pending-abc"
		scheduledNode  = "Node/gke-main-default-pool-abc"
		staticNodeDest = "Node/control-1"
	)

	assert.Equal(t, scheduledNode, runsOnBySource[scheduledID],
		"scheduled pod must have a RUNS_ON edge to its Node")
	assert.Equal(t, staticNodeDest, runsOnBySource[staticID],
		"static pod must emit RUNS_ON uniformly — the Node exists regardless of how the pod was created")

	_, unscheduledHasEdge := runsOnBySource[unscheduledID]
	assert.False(t, unscheduledHasEdge,
		"unscheduled pod (empty Spec.NodeName) must not emit a RUNS_ON edge")
}
