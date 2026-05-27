// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestFingerprintLabels_Deterministic(t *testing.T) {
	labels := map[string]string{"namespace": "prod", "service": "api", "env": "us-east"}
	fp1 := FingerprintLabels(labels)
	fp2 := FingerprintLabels(labels)
	assert.Equal(t, fp1, fp2, "same labels should produce same fingerprint")
	assert.Len(t, fp1, 64, "SHA-256 hex digest should be 64 chars")
}

func TestFingerprintLabels_OrderIndependent(t *testing.T) {
	// Two maps with the same entries in different insertion order.
	a := map[string]string{"z": "1", "a": "2", "m": "3"}
	b := map[string]string{"a": "2", "m": "3", "z": "1"}
	assert.Equal(t, FingerprintLabels(a), FingerprintLabels(b))
}

func TestFingerprintLabels_Empty(t *testing.T) {
	fp := FingerprintLabels(nil)
	assert.Len(t, fp, 64)
	assert.Equal(t, fp, FingerprintLabels(map[string]string{}))
}

func TestFingerprintLabels_DifferentValues(t *testing.T) {
	a := map[string]string{"key": "val1"}
	b := map[string]string{"key": "val2"}
	assert.NotEqual(t, FingerprintLabels(a), FingerprintLabels(b))
}

func TestNewLogStream(t *testing.T) {
	tracker := NewCardinalityTracker(3)
	// Make "pod" high-cardinality.
	tracker.Observe("pod", "pod-1")
	tracker.Observe("pod", "pod-2")
	tracker.Observe("pod", "pod-3")
	tracker.Observe("namespace", "prod")

	labels := map[string]string{
		"namespace": "prod",
		"pod":       "pod-99",
		"service":   "api",
	}
	s := NewLogStream(labels, tracker)

	// ID is fingerprint of ALL labels.
	assert.Equal(t, FingerprintLabels(labels), s.ID)
	// Fingerprint is based on low-card labels only.
	assert.Equal(t, FingerprintLabels(s.LowCardLabels), s.Fingerprint)

	// Classification.
	assert.Equal(t, "prod", s.LowCardLabels["namespace"])
	assert.Equal(t, "api", s.LowCardLabels["service"])
	assert.Equal(t, "pod-99", s.HighCardLabels["pod"])
}

func TestNewLogStream_SharedFingerprint(t *testing.T) {
	tracker := NewCardinalityTracker(3)
	tracker.Observe("pod", "pod-1")
	tracker.Observe("pod", "pod-2")
	tracker.Observe("pod", "pod-3")

	// Two streams with different pods but same low-card labels.
	s1 := NewLogStream(map[string]string{"namespace": "prod", "pod": "pod-a"}, tracker)
	s2 := NewLogStream(map[string]string{"namespace": "prod", "pod": "pod-b"}, tracker)

	// Different IDs (different full label sets).
	assert.NotEqual(t, s1.ID, s2.ID)
	// Same fingerprint (same low-card labels).
	assert.Equal(t, s1.Fingerprint, s2.Fingerprint)
}

func TestBuildLabelNodes(t *testing.T) {
	tracker := NewCardinalityTracker(100)
	tracker.Observe("namespace", "prod")
	tracker.Observe("service", "api")

	s1 := NewLogStream(map[string]string{"namespace": "prod", "service": "api"}, tracker)
	s2 := NewLogStream(map[string]string{"namespace": "prod", "service": "web"}, tracker)

	nodes := BuildLabelNodes([]*wirelogs.LogStream{s1, s2})

	// namespace=prod appears in both streams but should be deduplicated.
	ids := make(map[string]bool)
	for _, n := range nodes {
		ids[n.Id] = true
		assert.Equal(t, string(kgtypes.NodeLogLabel), n.Type)
		assert.NotEmpty(t, n.Metadata["label_key"])
		assert.NotEmpty(t, n.Metadata["label_value"])
	}

	assert.True(t, ids["log-label:namespace=prod"], "shared label node missing")
	assert.True(t, ids["log-label:service=api"], "service=api node missing")
	assert.True(t, ids["log-label:service=web"], "service=web node missing")
	assert.Len(t, nodes, 3, "namespace=prod should be deduplicated")
}

func TestBuildStreamNode(t *testing.T) {
	tracker := NewCardinalityTracker(100)
	labels := map[string]string{"namespace": "prod", "service": "api"}
	s := NewLogStream(labels, tracker)

	node := BuildStreamNode(s)

	assert.Equal(t, s.ID, node.Id)
	assert.Equal(t, string(kgtypes.NodeLogStream), node.Type)
	assert.Equal(t, "prod", node.Metadata["label:namespace"])
	assert.Equal(t, "api", node.Metadata["label:service"])
	assert.Equal(t, s.Fingerprint, node.Metadata["fingerprint"])
}

func TestBuildHasLabelEdges(t *testing.T) {
	tracker := NewCardinalityTracker(3)
	// Make "pod" high-cardinality so it gets no label node.
	tracker.Observe("pod", "pod-1")
	tracker.Observe("pod", "pod-2")
	tracker.Observe("pod", "pod-3")

	labels := map[string]string{
		"namespace": "prod",
		"service":   "api",
		"pod":       "pod-99",
	}
	s := NewLogStream(labels, tracker)

	// Simulate the ID map from BuildLabelNodes.
	labelNodeIDs := map[string]string{
		"namespace=prod": "log-label:namespace=prod",
		"service=api":    "log-label:service=api",
	}

	edges := BuildHasLabelEdges(s, labelNodeIDs)

	// Only low-card labels produce edges; pod is high-card.
	require.Len(t, edges, 2)
	for i := range edges {
		assert.Equal(t, s.ID, edges[i].FromId)
		assert.Equal(t, string(kgtypes.EdgeHasLabel), edges[i].Type)
	}

	// Collect target IDs to verify both labels are linked.
	targets := make(map[string]bool)
	for i := range edges {
		targets[edges[i].ToId] = true
	}
	assert.True(t, targets["log-label:namespace=prod"])
	assert.True(t, targets["log-label:service=api"])
}
