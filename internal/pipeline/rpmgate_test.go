// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestRPMGate covers the four behaviors of the fixed-rate pacer: a disabled
// gate is a no-op, sequential waits pace ~one interval apart, a ctx cancel
// aborts a pending wait promptly, and — the load-bearing case — N CONCURRENT
// waiters on one enabled gate stagger so total wall-clock is ~(N-1)·interval
// (a release-then-sleep bug would collapse this to ~one interval). Timing bands
// are generous to avoid CI flake.
func TestRPMGate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		// newRPMGate(0) is disabled: wait returns essentially immediately.
		g := newRPMGate(0)
		start := time.Now()
		g.wait(context.Background())
		g.wait(context.Background())
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 20*time.Millisecond, "disabled gate must not pace")
	})

	t.Run("sequential_pacing", func(t *testing.T) {
		// rpm=120 ⇒ 500ms interval. First grant is immediate; the second
		// sequential wait blocks ~one interval.
		g := newRPMGate(120)
		interval := time.Minute / 120 // 500ms

		start := time.Now()
		g.wait(context.Background()) // admitted immediately
		firstElapsed := time.Since(start)
		assert.Less(t, firstElapsed, interval/2, "first wait is admitted immediately")

		g.wait(context.Background()) // paced ~one interval after the first
		secondElapsed := time.Since(start)
		assert.GreaterOrEqual(t, secondElapsed, time.Duration(0.8*float64(interval)),
			"second sequential grant waits ~one interval")
	})

	t.Run("ctx_cancel", func(t *testing.T) {
		// A long interval would block for ~1 minute; ctx cancel mid-wait must
		// return promptly. The first wait reserves the slot immediately; the
		// second blocks until the (far-future) target — cancel aborts it.
		g := newRPMGate(1) // 60s interval
		g.wait(context.Background())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		g.wait(ctx)
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 500*time.Millisecond, "ctx cancel aborts the pending wait promptly")
	})

	t.Run("concurrent_staggering", func(t *testing.T) {
		// THE load-bearing test. N goroutines call wait(ctx) on one enabled gate
		// simultaneously. A correct reserve-then-wait gate forces them into
		// staggered slots (now+interval, now+2·interval, …) so the last waiter
		// wakes ~(N-1)·interval after launch. The naive release-then-sleep bug
		// lets all N read the same lastGrant and wake together, collapsing total
		// elapsed to ~one interval — this >= (N-1)·interval assertion is the only
		// one that distinguishes the two implementations.
		const n = 8
		g := newRPMGate(1200)          // 50ms interval
		interval := time.Minute / 1200 // 50ms
		floor := time.Duration(0.8 * float64(n-1) * float64(interval))

		var ready, done sync.WaitGroup
		ready.Add(n)
		done.Add(n)
		release := make(chan struct{})

		for range n {
			go func() {
				defer done.Done()
				ready.Done()
				<-release // all goroutines start their wait at the same moment
				g.wait(context.Background())
			}()
		}

		ready.Wait()
		start := time.Now()
		close(release)
		done.Wait()
		elapsed := time.Since(start)

		assert.GreaterOrEqual(t, elapsed, floor,
			"N concurrent waiters must stagger to ~(N-1)*interval; a release-then-sleep bug collapses to ~one interval")
	})
}
