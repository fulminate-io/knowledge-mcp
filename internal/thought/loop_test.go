// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// newPropagationLoopForTest constructs a PropagationLoop whose Start/Stop
// lifecycle can be exercised without sleeping for an hour or invoking
// real wire calls. interval is shrunk to milliseconds; backstopInterval sets the
// full-pass cadence (cadence tests pass a small value + a fake clock); onTick
// records each fire into a counter the test can read. gc is left nil because
// the test-injected onTick replaces the runBackgroundPropagation call
// path that would dereference it. The clock defaults to time.Now (cadence tests
// overwrite p.clock directly).
// admittedGate is the working-set gate every fixture whose SUBJECT is the pass
// body wires. Those tests assert what a tick does once knowledge/default has been
// admitted by a direct user interaction; without the gate seeded, the background
// entries short-circuit before the behavior under test runs. Fixtures that assert
// the GATE itself supply their own predicate instead.
func admittedGate() func() bool { return func() bool { return true } }

func newPropagationLoopForTest(interval, backstopInterval time.Duration, onTick func()) *PropagationLoop {
	p := &PropagationLoop{
		interval:         interval,
		backstopInterval: backstopInterval,
		clock:            time.Now,
		stopCh:           make(chan struct{}),
	}
	p.onTick = onTick
	return p
}

// TestPropagationLoop_HourlyTick exercises the OQ1 trigger semantics:
// every interval fire invokes the per-tick work exactly once, no
// charge-driven path is involved, and the production cadence constant
// is one hour.
func TestPropagationLoop_HourlyTick(t *testing.T) {
	t.Parallel()

	// Compile-time constant assertion: prod cadence is hourly per OQ1.
	if PropagationInterval != time.Hour {
		t.Fatalf("PropagationInterval = %v, want 1h (OQ1 lock)", PropagationInterval)
	}

	var ticks atomic.Int64
	p := newPropagationLoopForTest(5*time.Millisecond, time.Hour, func() {
		ticks.Add(1)
	})

	p.Start()
	defer p.Stop(time.Second)

	// Wait for at least 3 ticks to confirm the loop fires repeatedly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && ticks.Load() < 3 {
		time.Sleep(5 * time.Millisecond)
	}

	got := ticks.Load()
	if got < 3 {
		t.Fatalf("ticker fired %d times in 500ms (interval=5ms); want >=3", got)
	}
}

// TestPropagationLoop_StopUnwinds proves the T3-1 + T3-2 fixes hold:
// Stop() returns within its deadline AND no goroutines leak after the
// loop exits. Without the select{<-stopCh} branch added in T3-1, the
// goroutine would block forever on `for range ticker.C` and the
// goroutine-count assertion below would fail.
func TestPropagationLoop_StopUnwinds(t *testing.T) {
	t.Parallel()

	baseline := runtime.NumGoroutine()

	p := newPropagationLoopForTest(5*time.Millisecond, time.Hour, func() {
		// Simulate brief in-flight work the wg.Wait branch must drain.
		time.Sleep(2 * time.Millisecond)
	})
	p.Start()

	// Let the loop tick at least once so inFlight has something to drain
	// when Stop fires.
	time.Sleep(20 * time.Millisecond)

	stopReturned := make(chan struct{})
	go func() {
		p.Stop(time.Second)
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s — loop goroutine likely leaked")
	}

	// Give the runtime a few ticks to reclaim the loop goroutine's
	// stack so NumGoroutine reflects the post-Stop state.
	got := waitForGoroutineCount(baseline+1, 200*time.Millisecond)
	if got > baseline+1 {
		t.Errorf("goroutines after Stop = %d, baseline %d; loop goroutine leaked", got, baseline)
	}
}

// TestPropagationLoop_StopNilSafe locks in the dream.Runner.Stop-style
// nil-guard at PropagationLoop.Stop. wirePropagationRuntime degrading
// at boot leaves c.propLoop nil; the deferred c.propLoop.Stop(...) in
// the serve daemon's cleanup closure must not panic.
func TestPropagationLoop_StopNilSafe(t *testing.T) {
	t.Parallel()
	var p *PropagationLoop
	p.Stop(time.Second) // must not panic
}

// TestPropagationLoop_StopIdempotent confirms the stopOnce guard:
// repeated Stop calls don't double-close stopCh.
func TestPropagationLoop_StopIdempotent(t *testing.T) {
	t.Parallel()
	p := newPropagationLoopForTest(time.Hour, time.Hour, func() {})
	p.Start()
	p.Stop(time.Second)
	p.Stop(time.Second) // must not panic
}

// waitForGoroutineCount polls runtime.NumGoroutine until it reaches at
// most max or deadline expires. Mirrors the helper in
// cmd/knowledge-server/internal/store/registry_saver_test.go — the OS-thread Park bookkeeping
// behind NumGoroutine takes a tick to settle after a goroutine returns.
func waitForGoroutineCount(max int, deadline time.Duration) int {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if got := runtime.NumGoroutine(); got <= max {
			return got
		}
		time.Sleep(2 * time.Millisecond)
		runtime.Gosched()
	}
	return runtime.NumGoroutine()
}
