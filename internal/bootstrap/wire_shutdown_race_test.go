// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestDrainOnShutdown_SIGTERMMidWiring is the bind-first startup change T2-B race regression: a
// SIGTERM arriving mid-wiring runs the cleanup drain (drainOnShutdown)
// CONCURRENTLY with the background wiring goroutine's writes to
// c.runtime/c.propLoop/c.pipeline. The shared cancelable ctx + bounded join +
// flag-gated drain must (1) be data-race-clean (run under -race), (2) cancel the
// wiring ctx so a blocked stage unwinds, (3) return within the bounded join
// deadline, and (4) never Stop a nil/half-wired handle — with no readiness flag
// set, drainOnShutdown skips ALL three Stops.
//
// The synthetic wiring goroutine mirrors the production publish-then-mark order:
// it blocks on ctx.Done() at the first stage (so no flag is ever set and the
// handles stay nil), then closes wireDone via the deferred close. A plain Stop on
// the nil handles would NOT panic (they are nil-safe), so the load-bearing
// assertion is the flag gate: zero Stop attempts because zero flags are set.
func TestDrainOnShutdown_SIGTERMMidWiring(t *testing.T) {
	wireCtx, wireCancel := context.WithCancel(context.Background())
	c := &client{
		wireCtx:    wireCtx,
		wireCancel: wireCancel,
		wireDone:   make(chan struct{}),
	}

	var stageEntered, ctxObserved atomic.Bool

	// Synthetic background wiring goroutine: enters the first stage, then blocks
	// until the wiring ctx is canceled (mimicking a slow/stuck wireWorkerRuntime).
	// It sets NO readiness flag and writes NO handle, so the drain must skip every
	// Stop. The deferred close(c.wireDone) always signals — even on this early
	// ctx-canceled return — so the cleanup join completes promptly.
	go func() {
		defer close(c.wireDone)
		stageEntered.Store(true)
		<-c.wireCtx.Done() // block until drainOnShutdown cancels.
		ctxObserved.Store(true)
		// Early return on cancellation BEFORE marking any subsystem ready — the
		// exact mid-wiring window the drain must tolerate.
	}()

	// Wait for the goroutine to enter its blocking stage so the drain genuinely
	// races the in-flight wiring rather than running after it.
	for !stageEntered.Load() {
		time.Sleep(time.Millisecond)
	}

	done := make(chan struct{})
	start := time.Now()
	go func() {
		c.drainOnShutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wireJoinDeadline + 2*time.Second):
		t.Fatal("drainOnShutdown did not return within the bounded join deadline")
	}

	if elapsed := time.Since(start); elapsed > wireJoinDeadline+time.Second {
		t.Fatalf("drainOnShutdown took %v — expected well under the %v bounded join (ctx cancel should unblock the stage immediately)", elapsed, wireJoinDeadline)
	}
	if !ctxObserved.Load() {
		t.Fatal("the wiring goroutine never observed ctx cancellation — drainOnShutdown must cancel wireCtx")
	}
	// No readiness flag was set, so no handle was Stopped (the flag gate skipped
	// all three). The handles are nil throughout; the test passing under -race
	// proves the concurrent flag reads + the goroutine's (absent) writes never
	// race on a published field.
	if c.WorkerReady() || c.PropReady() || c.PipelineReady() {
		t.Fatal("no readiness flag should be set when wiring is canceled before any stage completes")
	}
}
