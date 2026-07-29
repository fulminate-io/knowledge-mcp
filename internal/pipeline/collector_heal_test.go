// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// healCollector builds a collector wired with a per-graph heal closure that
// records its call count, mirroring how Pipeline.RegisterGraph binds the closure
// (pipeline_collectors.go) so the test exercises the SAME wiring production uses.
// It returns the collector and a pointer to the live call counter. healErr, when
// non-nil, is returned by the closure to drive the best-effort error path.
func healCollector(gt kgtypes.GraphType, name string, healErr error) (*collector, *int) {
	calls := 0
	return &collector{
		gt:   gt,
		name: name,
		healIfSegmentless: func(context.Context) error {
			calls++
			return healErr
		},
	}, &calls
}

// TestMaybeHealCheck_FiresOnceOnEmbedDrain drives the embed-axis armed drain-edge
// decision (collector.maybeHealCheck) through a realistic INCREMENTAL sequence:
// the collect arms the latch, several ticks carry work / in-flight items, then the
// drain edge (empty scan, nothing in flight), then a run of post-drain idle empty
// ticks. Asserts the heal closure fires EXACTLY ONCE on the armed drain edge,
// disarms, and does NOT re-fire across the idle ticks.
func TestMaybeHealCheck_FiresOnceOnEmbedDrain(t *testing.T) {
	ctx := context.Background()
	c, calls := healCollector(kgtypes.GraphCode, "repo", nil)

	// The collect-wake armed the latch.
	armed := true
	// Ticks 1-3: work present / in flight — the latch STAYS armed; no heal yet.
	armed = c.maybeHealCheck(ctx, embedAxis, 8, 0, armed) // items present
	armed = c.maybeHealCheck(ctx, embedAxis, 0, 8, armed) // draining, in flight
	armed = c.maybeHealCheck(ctx, embedAxis, 3, 2, armed) // a late wave
	require.True(t, armed, "latch stays armed while work is present or in flight")
	require.Equal(t, 0, *calls, "no heal before the drain edge")

	// Tick 4: the armed embed drain-complete edge — empty scan AND nothing in flight.
	armed = c.maybeHealCheck(ctx, embedAxis, 0, 0, armed)
	require.False(t, armed, "latch disarms after the heal fires")
	require.Equal(t, 1, *calls, "exactly one heal check on the armed drain edge")

	// Ticks 5-8: post-drain idle empty scans — the latch is clear, so NO re-fire.
	for range 4 {
		armed = c.maybeHealCheck(ctx, embedAxis, 0, 0, armed)
	}
	require.Equal(t, 1, *calls, "no re-fire across post-drain idle empty ticks (once per collect)")
}

// TestMaybeHealCheck_NotOnSummaryAxis asserts the heal check is embed-axis only:
// an armed drain edge on the summary axis fires NOTHING.
func TestMaybeHealCheck_NotOnSummaryAxis(t *testing.T) {
	ctx := context.Background()
	c, calls := healCollector(kgtypes.GraphKnowledge, "kg", nil)

	armed := true
	armed = c.maybeHealCheck(ctx, summaryAxis, 5, 0, armed) // work present
	armed = c.maybeHealCheck(ctx, summaryAxis, 0, 0, armed) // drain edge
	require.Equal(t, 0, *calls, "summary axis never fires the heal check")
	// The latch is only cleared when the heal actually FIRES; on the summary axis
	// the firing is gated, so the latch is returned unchanged (harmless — it never fires).
	require.True(t, armed, "summary-axis latch is left unchanged (firing is gated, not the latch)")
}

// TestMaybeHealCheck_NilHealIsNoOp asserts a collector with no heal closure
// (no segment manager / heal factory wired — the common test/fake path) reaches
// the armed drain edge without panicking and fires nothing.
func TestMaybeHealCheck_NilHealIsNoOp(t *testing.T) {
	ctx := context.Background()
	c := &collector{gt: kgtypes.GraphCode, name: "repo"} // healIfSegmentless == nil
	// No heal is wired, so the firing is gated and the latch is returned unchanged;
	// the key property is the drain edge does not panic on a nil heal closure.
	require.NotPanics(t, func() {
		got := c.maybeHealCheck(ctx, embedAxis, 0, 0, true)
		require.True(t, got, "nil-heal armed drain leaves the latch unchanged (firing gated)")
	}, "armed drain edge with a nil heal closure must not panic")
}

// TestMaybeHealCheck_ErrorIsBestEffort asserts a heal error does NOT panic and
// still disarms the latch — the heal is best-effort (cheap probe + single-flight
// rebuild) and the next collect-armed drain retries, mirroring the quiescence
// flush error path.
func TestMaybeHealCheck_ErrorIsBestEffort(t *testing.T) {
	ctx := context.Background()
	c, calls := healCollector(kgtypes.GraphCode, "repo", errors.New("heal boom"))
	armed := c.maybeHealCheck(ctx, embedAxis, 0, 0, true)
	require.False(t, armed, "latch disarms even on a heal error (best-effort, per-collect arm)")
	require.Equal(t, 1, *calls, "the heal was attempted")
}

// TestMaybeHealCheck_DisarmsOnBreakerSentinel is the collector half of the v5 4.2
// heal-disarm test: a heal closure returning ErrHealDisarmed (the per-graph heal
// breaker latched) makes maybeHealCheck set the collector's healDisarmed flag, and the
// embed-wake arm site (gated on !healDisarmed) then stops re-arming — so the heal never
// fires again. RED against pre-fix code: maybeHealCheck ignores the sentinel, so
// healDisarmed stays false and every wake re-arms → the heal re-fires unbounded.
func TestMaybeHealCheck_DisarmsOnBreakerSentinel(t *testing.T) {
	ctx := context.Background()
	c, calls := healCollector(kgtypes.GraphCode, "repo", ErrHealDisarmed)

	// Model runLoop's per-wake cycle: a collect-wake re-arms the latch ONLY while the
	// collector is not disarmed (the arm-site gate `!c.healDisarmed.Load()` at
	// collector.go), then the drain edge consumes the latch via maybeHealCheck.
	armed := false
	wake := func() {
		if !c.healDisarmed.Load() {
			armed = true
		}
	}
	drain := func() { armed = c.maybeHealCheck(ctx, embedAxis, 0, 0, armed) }

	// Wake 1 → drain: the heal fires, returns ErrHealDisarmed, maybeHealCheck latches
	// healDisarmed and disarms.
	wake()
	require.True(t, armed, "the first wake armed the latch")
	drain()
	require.Equal(t, 1, *calls, "the heal fired once on the armed drain edge")
	require.True(t, c.healDisarmed.Load(), "the breaker sentinel latched the collector's healDisarmed flag")
	require.False(t, armed, "the drain disarmed the latch")

	// Subsequent wakes: the arm-site gate keeps the latch clear, so the heal never fires
	// again — the self-sustaining per-wake re-fire is broken.
	for range 5 {
		wake()
		drain()
	}
	require.Equal(t, 1, *calls, "no re-fire after the breaker disarmed the collector (RED: unbounded pre-fix)")
}
