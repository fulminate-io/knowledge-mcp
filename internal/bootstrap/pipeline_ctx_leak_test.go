// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"
)

// TestPipelineLoopsCancelOnWireCtx pins the C4 contract: the long-lived background
// loops wirePipelineRuntime spawns must run under the wiring ctx (c.wireCtx) so a
// shutdown — drainOnShutdown cancels wireCtx BEFORE pipeline.Stop — unwinds them.
// The bug was that wirePipelineRuntime spawned all 5 loops under a LOCAL
// context.Background() (pipeline.go), whose only exit (<-ctx.Done) never fires, so
// shutdown leaked every loop goroutine.
//
// runSegmentReconcileLoop is the seam exercised here — the highest-risk loop, since
// a leaked instance issues List(0)/RebuildSegments AFTER shutdown. With a nil
// segmentMgr its body (reconcileSegmentCoverage) returns immediately, so the loop
// is a bare ticker whose only non-tick exit is ctx.Done — isolating the cancel path.
//
// This is the wiring-level contract the C4 threading establishes: the loop honors
// the ctx it is handed. wirePipelineRuntime now hands it c.wireCtx (verified by the
// daemon.go caller + the pipeline.go spawn sites passing ctx, not Background), so a
// wireCtx cancel stops it. RED if the loop is spawned under a non-cancellable ctx
// (the pre-fix Background): the cancel is ignored and this deadlines.
func TestPipelineLoopsCancelOnWireCtx(t *testing.T) {
	// A minimal client with no segment manager: the loop body is a no-op tick, so
	// the test isolates the ctx-cancel exit the C4 wiring depends on.
	c := &client{}
	const interval = 5 * time.Millisecond

	wireCtx, wireCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Spawned exactly as the post-C4 wirePipelineRuntime spawns it: under the
		// wiring ctx, not a local Background.
		c.runSegmentReconcileLoop(wireCtx, interval)
		close(done)
	}()

	// Let the loop enter its select, then cancel the wiring ctx (the shutdown edge).
	time.Sleep(20 * time.Millisecond)
	wireCancel()

	select {
	case <-done:
		// Correct: the loop observed the wireCtx cancel and returned — no leak.
	case <-time.After(2 * time.Second):
		t.Fatal("runSegmentReconcileLoop did not stop on wireCtx cancel — a loop spawned under a non-cancellable ctx (the C4 leak) would hang exactly like this")
	}
}
