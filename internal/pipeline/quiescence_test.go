// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// quiescenceCollector builds a collector wired with a per-graph flush closure over
// the fake ship manager, mirroring how Pipeline.RegisterGraph binds the closure
// (pipeline_collectors.go) so the test exercises the SAME flush wiring production
// uses. embedAxis / summaryAxis are the minimal loopAxis values runLoop passes to
// maybeQuiescenceFlush (only the axis tag is read by the helper).
func quiescenceCollector(fsm *fakeShipManager, gt kgtypes.GraphType, name string) *collector {
	return &collector{
		gt:    gt,
		name:  name,
		flush: func(ctx context.Context) error { return fsm.Flush(ctx, gt, name) },
	}
}

var (
	embedAxis   = loopAxis{axis: "embed"}
	summaryAxis = loopAxis{axis: "summary"}
)

// TestQuiescenceFlush_FiresOnceOnEmbedDrain drives the embed-axis drain-edge
// decision (collector.maybeQuiescenceFlush — the exact logic runLoop calls after
// its push loop) through a realistic INCREMENTAL sequence: several ticks carrying
// sub-1024 work or in-flight items, then the drain edge (empty scan, nothing in
// flight), then a run of post-drain idle empty ticks. Asserts the per-graph
// quiescence Flush fires EXACTLY ONCE on the drain edge, is scoped to the right
// (gt, name), and does NOT re-fire across the idle ticks. Criteria:
// fires-exactly-once-after-drain + no-idle-re-flush.
func TestQuiescenceFlush_FiresOnceOnEmbedDrain(t *testing.T) {
	ctx := context.Background()
	fsm := &fakeShipManager{}
	c := quiescenceCollector(fsm, kgtypes.GraphCode, "repo")

	pending := false
	// Ticks 1-3: work present / in flight (the sub-1024 incremental backlog
	// draining). The latch arms; no flush yet.
	pending = c.maybeQuiescenceFlush(ctx, embedAxis, 8, 0, pending) // items pushed
	pending = c.maybeQuiescenceFlush(ctx, embedAxis, 0, 8, pending) // draining, still in flight
	pending = c.maybeQuiescenceFlush(ctx, embedAxis, 3, 2, pending) // a late wave
	require.True(t, pending, "latch is armed while work is present or in flight")
	require.Equal(t, 0, fsm.flushCalls, "no flush before the drain edge")

	// Tick 4: the drain-complete edge — empty scan AND nothing in flight.
	pending = c.maybeQuiescenceFlush(ctx, embedAxis, 0, 0, pending)
	require.False(t, pending, "latch clears after the flush fires")
	require.Equal(t, 1, fsm.flushCalls, "exactly one quiescence Flush on the drain edge")
	require.Equal(t,
		[]graphKey{{GraphType: kgtypes.GraphCode, GraphName: "repo"}},
		fsm.flushKeys, "flush is scoped to this collector's (gt, name)")

	// Ticks 5-8: post-drain idle empty scans — the latch is clear, so NO re-flush.
	for range 4 {
		pending = c.maybeQuiescenceFlush(ctx, embedAxis, 0, 0, pending)
	}
	require.Equal(t, 1, fsm.flushCalls, "no re-flush across post-drain idle empty ticks")
}

// TestQuiescenceFlush_NotOnSummaryAxis asserts the quiescence flush is embed-axis
// only: the same drain edge on the summary axis fires NOTHING (the summary axis
// has no segment tail to seal).
func TestQuiescenceFlush_NotOnSummaryAxis(t *testing.T) {
	ctx := context.Background()
	fsm := &fakeShipManager{}
	c := quiescenceCollector(fsm, kgtypes.GraphKnowledge, "kg")

	pending := false
	pending = c.maybeQuiescenceFlush(ctx, summaryAxis, 5, 0, pending) // work present
	pending = c.maybeQuiescenceFlush(ctx, summaryAxis, 0, 0, pending) // drain edge
	require.Equal(t, 0, fsm.flushCalls, "summary axis never fires the quiescence flush")
	// The latch is only cleared when the flush actually FIRES; on the summary axis
	// the firing is gated, so the latch is returned unchanged (harmless — it never fires).
	require.True(t, pending, "summary-axis latch is left unchanged (firing is gated, not the latch)")
}

// TestQuiescenceFlush_NilFlushIsNoOp asserts a collector with no flush closure
// (no segment manager wired — the common test/fake path) reaches the drain edge
// without panicking and fires nothing.
func TestQuiescenceFlush_NilFlushIsNoOp(t *testing.T) {
	ctx := context.Background()
	c := &collector{gt: kgtypes.GraphCode, name: "repo"} // flush == nil
	// No flush is wired, so the firing is gated and the latch is returned unchanged;
	// the key property is the drain edge does not panic on a nil flush closure.
	require.NotPanics(t, func() {
		c.maybeQuiescenceFlush(ctx, embedAxis, 0, 0, true)
	}, "drain edge with a nil flush closure must not panic")
}

// TestQuiescenceFlush_ErrorIsBestEffort asserts a flush error does NOT panic and
// still clears the latch — the sub-threshold tail being unsealed is non-fatal and
// the next drain retries (best-effort, mirroring the embed-writeback ship path).
func TestQuiescenceFlush_ErrorIsBestEffort(t *testing.T) {
	ctx := context.Background()
	fsm := &fakeShipManager{flushErr: errors.New("flush boom")}
	c := quiescenceCollector(fsm, kgtypes.GraphCode, "repo")
	pending := c.maybeQuiescenceFlush(ctx, embedAxis, 0, 0, true)
	require.False(t, pending, "latch clears even on a flush error")
	require.Equal(t, 1, fsm.flushCalls, "the flush was attempted")
}
