// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCircuitBreaker_TripsOnZeroSuccessWindow verifies the breaker latches
// paused only after tripThreshold consecutive errors with zero intervening
// success.
func TestCircuitBreaker_TripsOnZeroSuccessWindow(t *testing.T) {
	c := newCircuitBreaker(3)

	// Below threshold: not paused.
	c.record(false)
	c.record(false)
	if c.status().Paused {
		t.Fatalf("breaker tripped at 2 errors, threshold is 3")
	}
	// Third consecutive error trips.
	c.record(false)
	st := c.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip at 3 consecutive errors")
	}
	if st.Reason != autoTripReason {
		t.Fatalf("trip reason = %q, want %q", st.Reason, autoTripReason)
	}
	if st.Since.IsZero() {
		t.Fatalf("paused-since timestamp not set on trip")
	}
}

// TestCircuitBreaker_SuccessResetsCounter verifies that any success zeroes the
// shared counter so an isolated failure amid successes can never trip.
func TestCircuitBreaker_SuccessResetsCounter(t *testing.T) {
	c := newCircuitBreaker(3)

	// Interleave failures with a success every couple of failures: the
	// counter never reaches 3 because each success resets it.
	for range 50 {
		c.record(false)
		c.record(false)
		c.record(true) // resets to 0
		if c.status().Paused {
			t.Fatalf("breaker tripped despite an intervening success every 2 failures")
		}
	}
}

// TestCircuitBreaker_CrossAxisSuccessResets verifies the shared cross-axis
// counter: a success on one logical axis resets failures accumulated by the
// other (the breaker has no per-axis state — it is one shared counter).
func TestCircuitBreaker_CrossAxisSuccessResets(t *testing.T) {
	c := newCircuitBreaker(3)
	c.record(false) // "summary" fail
	c.record(false) // "embed" fail
	c.record(true)  // a success on EITHER axis resets the shared count
	c.record(false)
	c.record(false)
	if c.status().Paused {
		t.Fatalf("cross-axis success did not reset shared counter")
	}
}

// TestCircuitBreaker_WaitResumedSteadyState verifies waitResumed returns
// immediately when the breaker is not paused.
func TestCircuitBreaker_WaitResumedSteadyState(t *testing.T) {
	c := newCircuitBreaker(3)
	start := time.Now()
	c.waitResumed(context.Background())
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("steady-state waitResumed should be ~instant, took %v", d)
	}
}

// TestCircuitBreaker_WaitResumedBlocksUntilResume verifies a paused breaker
// blocks waitResumed until resume() is called.
func TestCircuitBreaker_WaitResumedBlocksUntilResume(t *testing.T) {
	c := newCircuitBreaker(1)
	c.record(false) // trips (threshold 1)
	if !c.status().Paused {
		t.Fatalf("breaker should be paused")
	}

	done := make(chan struct{})
	go func() {
		c.waitResumed(context.Background())
		close(done)
	}()

	// Should still be blocked shortly after.
	select {
	case <-done:
		t.Fatalf("waitResumed returned while still paused")
	case <-time.After(30 * time.Millisecond):
	}

	c.resume()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitResumed did not return after resume")
	}
	if c.status().Paused {
		t.Fatalf("breaker still paused after resume")
	}
}

// TestCircuitBreaker_WaitResumedHonorsContext verifies a worker parked in
// waitResumed on a paused breaker unblocks promptly on ctx cancel — this is
// what keeps Pipeline.Stop from hanging on the worker WaitGroup.
func TestCircuitBreaker_WaitResumedHonorsContext(t *testing.T) {
	c := newCircuitBreaker(1)
	c.pause("manual")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.waitResumed(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitResumed did not unblock on ctx cancel")
	}
}

// TestCircuitBreaker_ResumeZeroesCounter verifies resume clears the error
// accumulation so a resumed pool starts from a clean window.
func TestCircuitBreaker_ResumeZeroesCounter(t *testing.T) {
	c := newCircuitBreaker(3)
	c.record(false)
	c.record(false)
	c.record(false) // trips
	c.resume()
	// Two more failures must NOT re-trip immediately (counter was zeroed).
	c.record(false)
	c.record(false)
	if c.status().Paused {
		t.Fatalf("breaker re-tripped before threshold after resume zeroed counter")
	}
}

// TestCircuitBreaker_DefaultThreshold verifies a non-positive threshold falls
// back to the package default rather than tripping on the first error.
func TestCircuitBreaker_DefaultThreshold(t *testing.T) {
	c := newCircuitBreaker(0)
	if c.tripThreshold != DefaultCircuitBreakerThreshold {
		t.Fatalf("tripThreshold = %d, want default %d", c.tripThreshold, DefaultCircuitBreakerThreshold)
	}
	c.record(false)
	if c.status().Paused {
		t.Fatalf("breaker tripped on first error with default threshold")
	}
}

// TestCircuitBreaker_ConcurrentRecordResume drives record/waitResumed/resume
// concurrently to expose data races under -race.
func TestCircuitBreaker_ConcurrentRecordResume(t *testing.T) {
	c := newCircuitBreaker(5)
	ctx := t.Context()

	var wg sync.WaitGroup
	// Recorders churning failures + successes.
	for range 8 {
		wg.Go(func() {
			for range 200 {
				c.record(false)
				c.record(true)
			}
		})
	}
	// Waiters parking on the breaker.
	for range 8 {
		wg.Go(func() {
			for range 50 {
				c.waitResumed(ctx)
			}
		})
	}
	// A resumer continuously clearing any latch.
	wg.Go(func() {
		for range 200 {
			c.resume()
			time.Sleep(time.Millisecond)
		}
	})

	wg.Wait()
	// Final resume so the test ends in a known running state.
	c.resume()
	if c.status().Paused {
		t.Fatalf("breaker still paused after final resume")
	}
}
