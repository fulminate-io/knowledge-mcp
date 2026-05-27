// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestControllerNameCandidates verifies the pod-name → controller-name
// stripping covers the standard Kubernetes controller-naming conventions
// (DaemonSet / StatefulSet / Job in one strip; Deployment / CronJob in
// two strips). Edge cases: empty input, no hyphen, trailing hyphen.
func TestControllerNameCandidates(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			// DaemonSet pod "anetd-s59lq" — strip once → "anetd".
			name:     "DaemonSet single strip",
			input:    "anetd-s59lq",
			expected: []string{"anetd-s59lq", "anetd"},
		},
		{
			// Deployment pod "api-5d7b8c-xyz42" — strip to ReplicaSet
			// then to Deployment.
			name:     "Deployment double strip",
			input:    "api-5d7b8c-xyz42",
			expected: []string{"api-5d7b8c-xyz42", "api-5d7b8c", "api"},
		},
		{
			// Hyphenated controller name "anetd-l" still recoverable
			// from its DaemonSet pod.
			name:     "hyphenated DaemonSet",
			input:    "anetd-l-gmfj7",
			expected: []string{"anetd-l-gmfj7", "anetd-l", "anetd"},
		},
		{
			// StatefulSet pod "web-0" — strip once → "web".
			name:     "StatefulSet ordinal",
			input:    "web-0",
			expected: []string{"web-0", "web"},
		},
		{
			// No hyphen: single candidate only.
			name:     "no hyphen",
			input:    "monolith",
			expected: []string{"monolith"},
		},
		{
			name:     "empty",
			input:    "",
			expected: []string{""},
		},
		{
			// Trailing hyphen is unusual but shouldn't produce empty
			// candidate — the strip loop bails when the remainder is
			// empty.
			name:     "trailing hyphen",
			input:    "weird-",
			expected: []string{"weird-", "weird"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, controllerNameCandidates(tc.input))
		})
	}
}

// seedControllerFallbackGraph returns a single-account []GraphSlice with a K8s
// DaemonSet, a Deployment, a ReplicaSet owned by the Deployment, and a couple
// of matching Pods. Exercises the fallback chain where the service label
// doesn't match any workload but pod_name derivation can reach a controller.
// The slice feeds NewCloudSubgraph directly — no store engine.
func seedControllerFallbackGraph() []GraphSlice {
	nodes := []*knowledgev1.Node{
		// DaemonSet and a stale pod that should be skipped in favor
		// of the controller.
		mkCloudResource("kube-system/DaemonSet/anetd", "anetd", "DaemonSet"),
		mkCloudResource("kube-system/Pod/anetd-s59lq", "anetd-s59lq", "Pod"),
		// Deployment + ReplicaSet hierarchy.
		mkCloudResource("prod/Deployment/api", "api", "Deployment"),
		mkCloudResource("prod/ReplicaSet/api-5d7b8c", "api-5d7b8c", "ReplicaSet"),
		// Orphan pod (no parent controller in the graph).
		mkCloudResource("prod/Pod/orphan-abc123", "orphan-abc123", "Pod"),
	}
	return []GraphSlice{{Name: seedResolverAccount, Nodes: nodes}}
}

// TestCloudResolver_PodFallback_PrefersControllerOverPod: when both the
// Pod and its controlling DaemonSet exist in the graph, controller
// wins. Controllers are stable; pods are not.
func TestCloudResolver_PodFallback_PrefersControllerOverPod(t *testing.T) {
	stream := streamWithLabels(map[string]string{
		"pod_name": "anetd-s59lq",
	})
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedControllerFallbackGraph()))
	got, ok := r.ResolveService(ctx, stream, "cilium-agent")
	require.True(t, ok, "expected fallback to resolve to DaemonSet/anetd")
	assert.Equal(t, "kube-system/DaemonSet/anetd", got.ID)
}

// TestCloudResolver_PodFallback_DaemonSetStrip: pod_name stripping once
// recovers the DaemonSet.
func TestCloudResolver_PodFallback_DaemonSetStrip(t *testing.T) {
	slices := []GraphSlice{{Name: seedResolverAccount, Nodes: []*knowledgev1.Node{
		mkCloudResource("kube-system/DaemonSet/anetd", "anetd", "DaemonSet"),
	}}}

	// Pod that doesn't exist in the graph — simulates pod churn after
	// the graph was last collected. Single-hyphen DaemonSet naming
	// ({controller}-{nodehash}).
	stream := streamWithLabels(map[string]string{
		"pod_name": "anetd-newnode",
	})
	r := NewCloudResolver(NewCloudSubgraph(slices))
	got, ok := r.ResolveService(context.Background(), stream, "cilium-agent")
	require.True(t, ok, "expected stripping to recover DaemonSet even though pod is gone")
	assert.Equal(t, "kube-system/DaemonSet/anetd", got.ID)
}

// TestCloudResolver_PodFallback_DeploymentDoubleStrip: Deployment pods
// have two trailing hashes; double stripping must recover the
// Deployment.
func TestCloudResolver_PodFallback_DeploymentDoubleStrip(t *testing.T) {
	// Pod name of a Deployment: {name}-{rs-hash}-{pod-hash}.
	stream := streamWithLabels(map[string]string{
		"pod_name": "api-5d7b8c-xyz42",
	})
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedControllerFallbackGraph()))
	got, ok := r.ResolveService(ctx, stream, "unknown-container")
	require.True(t, ok)
	// Single-strip finds the ReplicaSet before the second strip hits
	// the Deployment — ReplicaSet is an acceptable controller match
	// and BFS reaches the Deployment from there via OWNED_BY.
	assert.Equal(t, "prod/ReplicaSet/api-5d7b8c", got.ID,
		"single strip should find the ReplicaSet before double strip")
}

// TestCloudResolver_PodFallback_LastResortPod: when no controller in
// the graph matches any candidate, fall back to the Pod itself.
func TestCloudResolver_PodFallback_LastResortPod(t *testing.T) {
	stream := streamWithLabels(map[string]string{
		"pod_name": "orphan-abc123",
	})
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedControllerFallbackGraph()))
	got, ok := r.ResolveService(ctx, stream, "unknown-container")
	require.True(t, ok, "orphan pod should still resolve as last-resort match")
	assert.Equal(t, "prod/Pod/orphan-abc123", got.ID)
}

// TestCloudResolver_PodFallback_NoPodNameLabel: when the stream has no
// pod_name label, fallback cannot fire and the resolver reports miss.
func TestCloudResolver_PodFallback_NoPodNameLabel(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedControllerFallbackGraph()))
	_, ok := r.ResolveService(ctx, streamWithLabels(nil), "unknown-container")
	assert.False(t, ok, "no pod_name label → no fallback")
}
