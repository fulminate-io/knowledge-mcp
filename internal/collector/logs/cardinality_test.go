// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCardinalityTracker_BasicTracking(t *testing.T) {
	ct := NewCardinalityTracker(5)

	ct.Observe("namespace", "prod")
	ct.Observe("namespace", "staging")
	ct.Observe("pod", "pod-abc123")

	assert.True(t, ct.IsLowCardinality("namespace"))
	assert.True(t, ct.IsLowCardinality("pod"))
	// Never-observed keys are low-cardinality.
	assert.True(t, ct.IsLowCardinality("unknown"))
}

func TestCardinalityTracker_ThresholdBehavior(t *testing.T) {
	ct := NewCardinalityTracker(3)

	// Below threshold.
	ct.Observe("env", "prod")
	ct.Observe("env", "staging")
	assert.True(t, ct.IsLowCardinality("env"))

	// At threshold -> no longer low-cardinality.
	ct.Observe("env", "dev")
	assert.False(t, ct.IsLowCardinality("env"))

	// Above threshold.
	ct.Observe("env", "test")
	assert.False(t, ct.IsLowCardinality("env"))
}

func TestCardinalityTracker_DefaultThreshold(t *testing.T) {
	ct := NewCardinalityTracker(0)
	// Should use DefaultCardinalityThreshold.
	for i := range DefaultCardinalityThreshold - 1 {
		ct.Observe("key", string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	assert.True(t, ct.IsLowCardinality("key"))
}

func TestCardinalityTracker_Classify(t *testing.T) {
	ct := NewCardinalityTracker(3)

	// Make "pod" high-cardinality.
	ct.Observe("pod", "pod-1")
	ct.Observe("pod", "pod-2")
	ct.Observe("pod", "pod-3")

	// Keep "namespace" low.
	ct.Observe("namespace", "prod")

	labels := map[string]string{
		"namespace": "prod",
		"pod":       "pod-99",
		"service":   "api",
	}
	lowCard, highCard := ct.Classify(labels)

	require.Len(t, lowCard, 2)
	assert.Equal(t, "prod", lowCard["namespace"])
	assert.Equal(t, "api", lowCard["service"])

	require.Len(t, highCard, 1)
	assert.Equal(t, "pod-99", highCard["pod"])
}
