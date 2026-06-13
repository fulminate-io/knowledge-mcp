// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"strings"
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
	c.recordErr(ClassOther)
	c.recordErr(ClassOther)
	if c.status().Paused {
		t.Fatalf("breaker tripped at 2 errors, threshold is 3")
	}
	// Third consecutive error trips.
	c.recordErr(ClassOther)
	st := c.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip at 3 consecutive errors")
	}
	// The auto-trip reason now names the dominant class of the window. All three
	// errors were ClassOther, so the reason reads "full error round — 3/3 errors"
	// (ClassOther.Label() == "errors"); a single distinct class adds no breakdown.
	wantReason := "full error round — 3/3 errors"
	if st.Reason != wantReason {
		t.Fatalf("trip reason = %q, want %q", st.Reason, wantReason)
	}
	if st.Since.IsZero() {
		t.Fatalf("paused-since timestamp not set on trip")
	}
}

// TestCircuitBreaker_RecordErrReturnsTrippedOnLatch pins the escalation seam the
// cross-axis coordinator (Pipeline.escalateOnTrip) consumes: recordErr returns
// true EXACTLY on the call that latches the auto-trip, and false otherwise (below
// threshold, or once already paused). It drives a NON-deterministic class
// (ClassTimeoutTransport) so the latch under test is the 3-call zero-success
// WINDOW — a deterministic class would fast-trip at 2 and exercise a different
// path (covered separately by the fast-trip tests).
func TestCircuitBreaker_RecordErrReturnsTrippedOnLatch(t *testing.T) {
	c := newCircuitBreaker(3)

	if c.recordErr(ClassTimeoutTransport) {
		t.Fatalf("recordErr #1 returned tripped, want false (below threshold)")
	}
	if c.recordErr(ClassTimeoutTransport) {
		t.Fatalf("recordErr #2 returned tripped, want false (below threshold)")
	}
	if !c.recordErr(ClassTimeoutTransport) {
		t.Fatalf("recordErr #3 returned false, want true (this call latches the trip)")
	}
	if c.recordErr(ClassTimeoutTransport) {
		t.Fatalf("recordErr #4 returned tripped, want false (already paused)")
	}
}

// TestCircuitBreaker_SuccessResetsCounter verifies that any success zeroes the
// breaker's counter so an isolated failure amid successes can never trip.
func TestCircuitBreaker_SuccessResetsCounter(t *testing.T) {
	c := newCircuitBreaker(3)

	// Interleave failures with a success every couple of failures: the
	// counter never reaches 3 because each success resets it.
	for range 50 {
		c.recordErr(ClassOther)
		c.recordErr(ClassOther)
		c.recordOK() // resets to 0
		if c.status().Paused {
			t.Fatalf("breaker tripped despite an intervening success every 2 failures")
		}
	}
}

// TestCircuitBreaker_SuccessResetsThenFailuresResume verifies a single
// circuitBreaker instance's counter mechanics: a success zeroes the counter, so
// failures accumulated before it do not carry forward — two fresh post-success
// failures stay below the threshold (one breaker, one counter; no axis framing,
// since each axis owns its own independent instance).
func TestCircuitBreaker_SuccessResetsThenFailuresResume(t *testing.T) {
	c := newCircuitBreaker(3)
	c.recordErr(ClassOther) // fail
	c.recordErr(ClassOther) // fail
	c.recordOK()            // a success zeroes this breaker's counter
	c.recordErr(ClassOther)
	c.recordErr(ClassOther)
	if c.status().Paused {
		t.Fatalf("success did not reset the breaker's counter")
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
	c.recordErr(ClassOther) // trips (threshold 1)
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
	c.recordErr(ClassOther)
	c.recordErr(ClassOther)
	c.recordErr(ClassOther) // trips
	c.resume()
	// Two more failures must NOT re-trip immediately (counter was zeroed).
	c.recordErr(ClassOther)
	c.recordErr(ClassOther)
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
	c.recordErr(ClassOther)
	if c.status().Paused {
		t.Fatalf("breaker tripped on first error with default threshold")
	}
}

// TestCircuitBreaker_FastTripOnConsecutiveDeterministic verifies the
// deterministic same-class fast-trip: two consecutive ClassParse failures latch
// the breaker at DefaultDeterministicFastTripThreshold (2) — far below the
// 20-call zero-success window. The reason NAMES the parse class (via
// ErrClass.Label()) and is NOT the full-round window reason. RED if the streak
// were class-agnostic (it would then need the full window threshold to trip).
func TestCircuitBreaker_FastTripOnConsecutiveDeterministic(t *testing.T) {
	c := newCircuitBreaker(DefaultCircuitBreakerThreshold) // 20-call window
	c.recordErr(ClassParse)
	if c.status().Paused {
		t.Fatalf("breaker fast-tripped on the first parse failure; threshold is %d", DefaultDeterministicFastTripThreshold)
	}
	c.recordErr(ClassParse)
	st := c.status()
	if !st.Paused {
		t.Fatalf("breaker did not fast-trip after %d consecutive parse failures", DefaultDeterministicFastTripThreshold)
	}
	if !strings.Contains(st.Reason, ClassParse.Label()) {
		t.Fatalf("fast-trip reason %q does not name the parse class label %q", st.Reason, ClassParse.Label())
	}
	if strings.Contains(st.Reason, "full error round") {
		t.Fatalf("fast-trip reason %q regressed to the full-round window reason", st.Reason)
	}
}

// TestCircuitBreaker_FastTripOnConsecutiveTruncation pins the orchestrator ruling
// that truncation is deterministic-terminal: two consecutive ClassTruncation
// failures fast-trip and the reason NAMES truncation. RED if
// IsDeterministicTerminal omits ClassTruncation.
func TestCircuitBreaker_FastTripOnConsecutiveTruncation(t *testing.T) {
	c := newCircuitBreaker(DefaultCircuitBreakerThreshold)
	c.recordErr(ClassTruncation)
	c.recordErr(ClassTruncation)
	st := c.status()
	if !st.Paused {
		t.Fatalf("breaker did not fast-trip after %d consecutive truncation failures", DefaultDeterministicFastTripThreshold)
	}
	if !strings.Contains(st.Reason, ClassTruncation.Label()) {
		t.Fatalf("fast-trip reason %q does not name the truncation class label %q", st.Reason, ClassTruncation.Label())
	}
}

// TestCircuitBreaker_InterleavedTransientDoesNotFastTrip verifies a
// non-deterministic errored call between two same-class deterministic failures
// resets the streak, so no fast-trip occurs. (The breaker uses the default
// 20-window so the three errored calls also stay well under the window.)
func TestCircuitBreaker_InterleavedTransientDoesNotFastTrip(t *testing.T) {
	c := newCircuitBreaker(DefaultCircuitBreakerThreshold)
	c.recordErr(ClassParse)
	c.recordErr(ClassTimeoutTransport) // non-deterministic: resets the streak
	c.recordErr(ClassParse)
	if c.status().Paused {
		t.Fatalf("breaker fast-tripped despite a non-deterministic call resetting the streak")
	}
}

// TestCircuitBreaker_DifferentDeterministicClassResets is a reset matrix: two
// DIFFERENT-class deterministic failures must NOT fast-trip (the ticket requires
// the SAME class; a different deterministic class restarts the streak at 1).
func TestCircuitBreaker_DifferentDeterministicClassResets(t *testing.T) {
	rows := []struct {
		name   string
		first  ErrClass
		second ErrClass
	}{
		{"parse_then_invalid", ClassParse, ClassInvalidRequest},
		{"parse_then_truncation", ClassParse, ClassTruncation},
		{"truncation_then_parse", ClassTruncation, ClassParse},
		{"invalid_then_truncation", ClassInvalidRequest, ClassTruncation},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := newCircuitBreaker(DefaultCircuitBreakerThreshold)
			c.recordErr(row.first)
			c.recordErr(row.second)
			if c.status().Paused {
				t.Fatalf("breaker fast-tripped on a different-class deterministic pair %v->%v; same class is required", row.first, row.second)
			}
		})
	}
}

// TestCircuitBreaker_SuccessResetsDeterministicStreak verifies an intervening
// success clears the deterministic streak so a following same-class failure does
// not fast-trip.
func TestCircuitBreaker_SuccessResetsDeterministicStreak(t *testing.T) {
	c := newCircuitBreaker(DefaultCircuitBreakerThreshold)
	c.recordErr(ClassParse)
	c.recordOK() // success ends the streak
	c.recordErr(ClassParse)
	if c.status().Paused {
		t.Fatalf("breaker fast-tripped despite an intervening success resetting the streak")
	}
}

// TestCircuitBreaker_ResumeClearsDeterministicStreak verifies resume() zeroes the
// deterministic streak: after a fast-trip + resume, a single same-class failure
// must NOT re-fast-trip on a carried-over count (recovery obligation).
func TestCircuitBreaker_ResumeClearsDeterministicStreak(t *testing.T) {
	c := newCircuitBreaker(DefaultCircuitBreakerThreshold)
	c.recordErr(ClassParse)
	c.recordErr(ClassParse) // fast-trips
	if !c.status().Paused {
		t.Fatalf("breaker did not fast-trip before resume")
	}
	c.resume()
	c.recordErr(ClassParse) // one post-resume failure must not re-trip
	if c.status().Paused {
		t.Fatalf("breaker re-fast-tripped on one post-resume parse failure; resume must zero the streak")
	}
}

// TestCircuitBreaker_BothCountersAdvance pins the both-counters invariant: a
// single recordErr(ClassParse) below the fast-trip threshold still advances the
// class-agnostic zero-success window by exactly 1. The breaker is built with a
// tripThreshold of 2 so a SINGLE parse error does not fast-trip (streak needs 2)
// and does not trip the window (needs 2), then a SECOND errored call of a
// DIFFERENT class (so the deterministic streak resets, never reaching 2) trips
// the window at exactly 2 — proving the first deterministic call was counted in
// consecErrors.
func TestCircuitBreaker_BothCountersAdvance(t *testing.T) {
	c := newCircuitBreaker(2) // window threshold 2
	c.recordErr(ClassParse)   // window=1, deterministic streak=1 (neither trips yet)
	if c.status().Paused {
		t.Fatalf("breaker tripped on the first parse error; both thresholds are 2")
	}
	c.recordErr(ClassTimeoutTransport) // window=2 -> window trips; streak reset to 0
	st := c.status()
	if !st.Paused {
		t.Fatalf("window did not trip at 2 errored calls — the first parse call was not counted in consecErrors")
	}
	// The trip is the full-round window reason (not the fast-trip), since the
	// streak was reset by the different-class second call.
	if !strings.Contains(st.Reason, "full error round") {
		t.Fatalf("expected the window (full error round) reason, got %q", st.Reason)
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
				c.recordErr(ClassOther)
				c.recordOK()
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
