// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// blockingCaller's Execute blocks until the per-pass ctx is cancelled, then
// records that it observed cancellation. It is the in-flight pass body for the
// cooperative-drain test: a pass that honors ctx (every real wire call does, via
// the connect-go transport) unwinds promptly when Stop cancels baseCtx.
type blockingCaller struct {
	entered  atomic.Bool
	canceled atomic.Bool
}

func (c *blockingCaller) Execute(ctx context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.entered.Store(true)
	<-ctx.Done() // block until baseCancel (via the per-pass derived ctx) fires.
	c.canceled.Store(true)
	return nil, ctx.Err()
}

// TestPropagationLoop_StopDrainsInFlightPass (FAILS-WHEN-ABSENT) is the bind-first startup change
// In-Scope item 4 guard: with a pass IN FLIGHT whose body blocks until ctx
// cancellation, PropagationLoop.Stop(shortDeadline) must RETURN within a tight
// bound (well under the 5/6-minute pass budget) AND the in-flight pass must
// observe cancellation. Pre-fix the pass ran on context.Background(), so Stop
// burned the full deadline and the pass never saw cancellation — this test would
// hang past the short deadline / report canceled=false.
func TestPropagationLoop_StopDrainsInFlightPass(t *testing.T) {
	gc := &blockingCaller{}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel() // idempotent second cancel; Stop also cancels via p.baseCancel
	p := &PropagationLoop{
		gc:         gc,
		stopCh:     make(chan struct{}),
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
		clock:      time.Now,
		admitted:   admittedGate(),
	}

	// Launch a real pass in flight: runBackgroundPropagation claims the
	// single-flight guard, adds the inFlight bracket, and drives runPass →
	// runClusterDetection → fetchAdjacency → gc.Execute, which blocks on ctx.
	passReturned := make(chan struct{})
	go func() {
		p.runBackgroundPropagation()
		close(passReturned)
	}()

	// Wait until the pass is genuinely in flight (Execute entered) so Stop races a
	// live pass, not a not-yet-started one.
	deadline := time.Now().Add(2 * time.Second)
	for !gc.entered.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the in-flight pass never reached gc.Execute")
		}
		time.Sleep(time.Millisecond)
	}

	// Stop with a SHORT deadline. The cooperative-drain fix cancels baseCtx first,
	// so the blocked Execute unwinds and inFlight.Wait completes well under the
	// deadline. A non-cooperative pass would force Stop to wait the full deadline.
	const shortDeadline = 2 * time.Second
	stopReturned := make(chan struct{})
	start := time.Now()
	go func() {
		p.Stop(shortDeadline)
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
	case <-time.After(shortDeadline + time.Second):
		t.Fatal("Stop did not return — the in-flight pass ignored ctx cancellation (pre-fix behavior)")
	}

	if elapsed := time.Since(start); elapsed >= shortDeadline {
		t.Fatalf("Stop took %v (>= the %v deadline) — it burned the deadline instead of cooperatively draining", elapsed, shortDeadline)
	}

	// The pass goroutine must have unwound (Execute returned on ctx cancel).
	select {
	case <-passReturned:
	case <-time.After(time.Second):
		t.Fatal("the in-flight pass goroutine did not return after Stop")
	}
	if !gc.canceled.Load() {
		t.Fatal("the in-flight pass never observed ctx cancellation — baseCancel did not reach the pass ctx")
	}
}
