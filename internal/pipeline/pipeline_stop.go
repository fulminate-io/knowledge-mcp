// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"sync"
)

// Stop runs the 7-step shutdown sequence per ticket Section D. Returns
// when (a) every goroutine has exited or (b) ctx fires, whichever first.
// Idempotent via stopOnce.
//
// Sequence:
//  1. Cancel every collector context (collectors stop pushing).
//  2. Wait collectorWG (collectors fully exited; no new work in flight).
//  3. Close summaryCh + embedCh (dispatchers see EOF, drain partial batches).
//  4. Wait dispatcherWG (dispatchers exited; sub-channels won't see new sends).
//  5. Close summaryBatchCh + embedBatchCh (workers see EOF, drain final batches).
//  6. Wait workerWG (workers exited).
//  7. Return.
//
// Axis-off case (an axis's LLM function not configured — summaryEnabled /
// embedEnabled false): that axis's dispatcher + worker pool were never started
// and no collector runs its loop, so nothing ever sends on that axis's
// channels. Step 3's close(summaryCh) / close(embedCh) is still safe (closing a
// channel with no senders and no readers is a no-op cleanup); the per-batch
// sub-channel is normally closed by that axis's dispatcher defer, which never
// runs for a disabled axis, but because no worker for that axis waits on it the
// left-open channel hangs nothing. dispatcherWG / workerWG carry no count for
// the un-started goroutines (Start's Add is gated on the same flags), so Steps
// 4 + 6 cannot block on a goroutine that never started. This holds for either
// axis disabled, both disabled, or both enabled.
//
// stopOnce.Do guards against double-close panics if Stop is invoked
// concurrently from server-shutdown + test-cleanup paths.
func (p *Pipeline) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() { p.stopErr = p.stopSequence(ctx) })
	return p.stopErr
}

// stopSequence is the body of Stop, factored out so the Stop method
// itself stays under the 80-line cap.
func (p *Pipeline) stopSequence(ctx context.Context) error {
	// Step 1: cancel every collector, and LATCH THE REGISTRY CLOSED in the same
	// critical section.
	//
	// THE LATCH IS WHAT MAKES STEP 2 TERMINATE. Without it a RegisterGraph taking
	// this mutex after the Unlock below stores its cancel func in the fresh map
	// nothing reads again and enlists its goroutine on the WaitGroup step 2 is about
	// to wait on — so step 2 waits for a goroutine this function has no way to
	// cancel. Setting the latch here, under the same lock as the cancel-and-clear,
	// means a registration either happened BEFORE the snapshot (and was cancelled)
	// or is refused. There is no third case.
	//
	// IT ALSO REMOVES A HIDDEN ORDERING REQUIREMENT ON CALLERS. The daemon's
	// shutdown happens to cancel the wiring ctx before calling Stop, which stops the
	// refresh loop that drives registrations, so production mostly escaped this.
	// That was a property of ONE caller rather than of Stop, and nothing in Stop's
	// contract said so; a caller stopping a pipeline whose refresh loop is still
	// live had no way to know it was required.
	p.collectorMu.Lock()
	p.collectorsStopped = true
	for _, cancel := range p.collectorCancels {
		cancel()
	}
	p.collectorCancels = make(map[graphKey]context.CancelFunc)
	p.collectorWakes = make(map[graphKey][]chan struct{})
	p.collectorMu.Unlock()

	// Step 2: wait collectors, bounded by ctx.
	if err := waitWithCtx(ctx, &p.collectorWG); err != nil {
		return fmt.Errorf("pipeline: collectors did not drain: %w", err)
	}

	// Step 3: close summary + embed channels (dispatcher EOF).
	close(p.summaryCh)
	close(p.embedCh)

	// Step 4: wait dispatchers. Note: dispatcher functions close their
	// own output (batch) channels via deferred close — Step 5 below is
	// implicit, NOT an explicit close here (would be double-close).
	if err := waitWithCtx(ctx, &p.dispatcherWG); err != nil {
		return fmt.Errorf("pipeline: dispatchers did not drain: %w", err)
	}

	// Step 5: per-batch sub-channels are closed by dispatcher's defer.
	// Step 6: wait workers (they observe EOF via the dispatcher's close).
	//
	// EACH WAIT NAMES ITS OWN STAGE, and that is the whole reason these three
	// returns are wrapped. The bare ctx error the caller used to receive said that
	// something did not drain inside the window and never which — collectors still
	// pushing, dispatchers still batching, or workers still calling an LLM — which
	// are three different operational problems with three different remedies.
	if err := waitWithCtx(ctx, &p.workerWG); err != nil {
		return fmt.Errorf("pipeline: workers did not drain: %w", err)
	}
	return nil
}

// waitWithCtx waits for wg to reach zero or ctx to fire. Returns ctx.Err
// on timeout / cancel.
func waitWithCtx(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
