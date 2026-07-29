// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_heal_breaker_test.go covers the per-(graphType, name) heal breaker state
// machine in isolation, mirroring the SHAPES of pipeline/circuit_breaker_test.go
// (TripsOnZeroSuccessWindow / SuccessResetsCounter / ResumeZeroesCounter): trip-after-K,
// latch (stays disarmed), no-self-heal, RecordProgress resets pre-trip, ClearHealLatch
// re-arms post-trip, and per-graph independence.

// TestSegmentHealBreaker_TripsAfterKNoProgress verifies the breaker latches disarmed
// after exactly healBreakerTripThreshold consecutive no-progress passes, and that
// RecordNoProgress returns tripped == true EXACTLY on the latching call.
func TestSegmentHealBreaker_TripsAfterKNoProgress(t *testing.T) {
	var b segmentHealBreaker
	gt, name := kgtypes.GraphCode, "repo"

	require.True(t, b.Allow(gt, name), "a fresh breaker allows heal")
	// The first threshold-1 no-progress passes do NOT trip.
	for i := range healBreakerTripThreshold - 1 {
		require.False(t, b.RecordNoProgress(gt, name), "no-progress pass %d is below the trip threshold", i+1)
		require.True(t, b.Allow(gt, name), "still armed below threshold")
	}
	// The threshold-th consecutive no-progress pass latches.
	require.True(t, b.RecordNoProgress(gt, name), "the threshold-th no-progress pass latches the breaker")
	require.False(t, b.Allow(gt, name), "a latched breaker disallows heal")
}

// TestSegmentHealBreaker_LatchAndNoSelfHeal verifies that once latched the breaker
// STAYS latched: further RecordNoProgress does not re-trip (returns false), and a
// RecordProgress does NOT un-latch it (no self-heal — only ClearHealLatch or restart
// re-arms).
func TestSegmentHealBreaker_LatchAndNoSelfHeal(t *testing.T) {
	var b segmentHealBreaker
	gt, name := kgtypes.GraphCode, "repo"

	for range healBreakerTripThreshold {
		b.RecordNoProgress(gt, name)
	}
	require.False(t, b.Allow(gt, name), "latched after K no-progress passes")

	// Further no-progress does not re-trip.
	require.False(t, b.RecordNoProgress(gt, name), "an already-latched breaker does not re-trip")
	require.False(t, b.Allow(gt, name), "still latched")

	// RecordProgress must NOT self-heal a latched breaker.
	b.RecordProgress(gt, name)
	require.False(t, b.Allow(gt, name), "RecordProgress does not un-latch a tripped breaker (no self-heal)")
}

// TestSegmentHealBreaker_RecordProgressResetsPreTrip verifies a progress pass resets
// the no-progress streak BEFORE the trip, so it then takes a full fresh run of
// healBreakerTripThreshold no-progress passes to latch.
func TestSegmentHealBreaker_RecordProgressResetsPreTrip(t *testing.T) {
	if healBreakerTripThreshold < 2 {
		t.Skip("threshold must be >= 2 for the reset-before-trip semantics to be observable")
	}
	var b segmentHealBreaker
	gt, name := kgtypes.GraphCode, "repo"

	// One no-progress (streak 1), then progress resets the streak to 0.
	require.False(t, b.RecordNoProgress(gt, name))
	b.RecordProgress(gt, name)

	// It now takes a FULL fresh run to trip — the reset dropped the earlier streak.
	for i := range healBreakerTripThreshold - 1 {
		require.False(t, b.RecordNoProgress(gt, name), "post-reset no-progress pass %d below threshold", i+1)
	}
	require.True(t, b.Allow(gt, name), "still armed: the progress pass reset the streak")
	require.True(t, b.RecordNoProgress(gt, name), "the threshold-th post-reset no-progress pass latches")
	require.False(t, b.Allow(gt, name))
}

// TestSegmentHealBreaker_ClearHealLatchReArms verifies ClearHealLatch clears the latch
// AND the streak post-trip, re-arming the graph (the manual rebuild_segments re-arm).
func TestSegmentHealBreaker_ClearHealLatchReArms(t *testing.T) {
	var b segmentHealBreaker
	gt, name := kgtypes.GraphCode, "repo"

	for range healBreakerTripThreshold {
		b.RecordNoProgress(gt, name)
	}
	require.False(t, b.Allow(gt, name), "latched")

	b.ClearHealLatch(gt, name)
	require.True(t, b.Allow(gt, name), "ClearHealLatch re-arms the breaker")

	// And the streak was reset: it takes a full fresh run to re-latch.
	for i := range healBreakerTripThreshold - 1 {
		require.False(t, b.RecordNoProgress(gt, name), "post-clear no-progress pass %d below threshold", i+1)
	}
	require.True(t, b.Allow(gt, name), "still armed after fewer than threshold post-clear no-progress passes")
	require.True(t, b.RecordNoProgress(gt, name), "re-latches after a full fresh run")
}

// TestSegmentHealBreaker_PerGraphIndependence verifies latching one (gt, name) leaves
// another graph unaffected — the latch is strictly per (graphType, name).
func TestSegmentHealBreaker_PerGraphIndependence(t *testing.T) {
	var b segmentHealBreaker
	a := struct {
		gt   kgtypes.GraphType
		name string
	}{kgtypes.GraphCode, "repoA"}
	other := struct {
		gt   kgtypes.GraphType
		name string
	}{kgtypes.GraphKnowledge, "default"}

	for range healBreakerTripThreshold {
		b.RecordNoProgress(a.gt, a.name)
	}
	require.False(t, b.Allow(a.gt, a.name), "graph A latched")
	require.True(t, b.Allow(other.gt, other.name), "a different graph is unaffected by A's latch")

	// A same-name graph of a DIFFERENT type is also independent (the key includes gt).
	require.True(t, b.Allow(kgtypes.GraphKnowledge, "repoA"), "same name, different graph type is a distinct latch key")
}
