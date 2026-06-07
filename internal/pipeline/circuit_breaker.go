// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"time"
)

// autoTripReason is the fixed reason string the breaker stamps when it trips
// itself on a zero-success storm. It is the exact text rendered by
// pipeline_status and the search staleness footer, so it must read for a human
// operator. It deliberately does NOT name quota/auth exclusively: local
// cli_deadline timeouts (subprocess.go) stay transient and so also feed the
// failure window, meaning a repeated-timeout storm can trip the breaker too.
const autoTripReason = "full error round (quota/auth or repeated timeouts)"

// circuitBreaker is a latched pause gate guarding every summary + embed
// worker. It is the sibling of errBackoff: where the backoff gate throttles a
// transient rate limit with a self-clearing time window, the breaker LATCHES
// the whole worker pool to a paused state on a zero-success storm and stays
// there until a human resumes — there is NO self-heal and NO auto-probe.
//
// Trip semantics — the ZERO-SUCCESS WINDOW. The invariant: trip iff a window
// of observed LLM-call results has had >= 1 attempt and ZERO successes. A
// "window" is the run of call results since the last success. It is realized
// with a single SHARED consecutive-error counter:
//
//   - record(true)  zeroes consecErrors — a success on EITHER axis proves the
//     pipeline is not fully dead, so it resets the failure accumulation.
//   - record(false) increments consecErrors and trips only once it reaches
//     tripThreshold with NOT ONE intervening success.
//
// Because every success zeroes the counter, an isolated failure amid
// concurrent successes is MATHEMATICALLY UNABLE to trip: the intervening
// successes keep resetting consecErrors below threshold. The breaker fires
// only when tripThreshold consecutive errored calls land with zero successes
// across either axis — a genuine quota / auth / repeated-timeout storm where
// everything in flight is failing right now.
//
// Cross-axis coupling is INTENTIONAL. The single shared consecErrors counter
// spans BOTH the summary and embed axes deliberately. The protected resource
// (provider quota / subscription / auth) is global — exactly like errBackoff's
// shared window — so a success on summary OR embed is sufficient evidence the
// pipeline is alive and resets the shared count; conversely a full-round storm
// shows as zero successes on BOTH axes. Pausing is likewise global: both axes'
// wait sites gate on waitResumed, so a trip stops all work at once.
type circuitBreaker struct {
	tripThreshold int

	mu           sync.Mutex
	paused       bool
	reason       string
	pausedAt     time.Time
	consecErrors int
	// resumed is closed (and recreated) on every pause->resume transition to
	// wake all goroutines parked in waitResumed. It is nil when not paused.
	resumed chan struct{}
}

// circuitStatus is the snapshot returned by status(); it carries everything
// pipeline_status and the staleness footer need to render the paused line.
type circuitStatus struct {
	Paused bool
	Reason string
	Since  time.Time
}

// newCircuitBreaker constructs the gate. tripThreshold is the number of
// consecutive errored LLM calls (with zero intervening success across either
// axis) that latches the pool paused. A non-positive threshold falls back to a
// safe default so a zero-value Config never produces a degenerate gate that
// trips on the first error.
func newCircuitBreaker(tripThreshold int) *circuitBreaker {
	if tripThreshold <= 0 {
		tripThreshold = DefaultCircuitBreakerThreshold
	}
	return &circuitBreaker{tripThreshold: tripThreshold}
}

// record observes one LLM-call outcome. A success zeroes the shared
// consecutive-error counter (the steady-state hot path is a cheap no-op when
// already at zero). A failure increments it and, on reaching tripThreshold
// while not already paused, latches the pool paused with the auto-trip reason.
// Both the transient and terminal error branches of the worker error handlers
// call record(false): the window counts every errored call, not just terminal
// ones, so a repeated-timeout storm trips just as a quota storm does.
func (c *circuitBreaker) record(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.consecErrors = 0
		return
	}
	c.consecErrors++
	if !c.paused && c.consecErrors >= c.tripThreshold {
		c.tripLocked(autoTripReason)
	}
}

// waitResumed blocks while the breaker is paused, returning when the pool is
// resumed or ctx is canceled. It returns immediately (single mutex acquire) in
// the steady state. It NEVER holds the lock while blocked: the ctx.Done() path
// is what lets a worker parked here unblock on Pipeline.Stop / ctx-cancel, so
// the worker WaitGroup never hangs on shutdown.
func (c *circuitBreaker) waitResumed(ctx context.Context) {
	for {
		c.mu.Lock()
		if !c.paused {
			c.mu.Unlock()
			return
		}
		ch := c.resumed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ch:
			// Resumed (or re-paused since); loop to re-check under the lock.
		}
	}
}

// pause latches the pool paused with an operator-supplied reason. Idempotent:
// pausing an already-paused breaker refreshes the reason but keeps the
// original pausedAt and does not disturb parked waiters.
func (c *circuitBreaker) pause(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tripLocked(reason)
}

// tripLocked sets the paused state. Caller holds c.mu. Idempotent on an
// already-paused breaker except for refreshing the reason; the broadcast
// channel is created on the first transition into paused so waiters have
// something to select on.
func (c *circuitBreaker) tripLocked(reason string) {
	c.reason = reason
	if c.paused {
		return
	}
	c.paused = true
	c.pausedAt = time.Now()
	if c.resumed == nil {
		c.resumed = make(chan struct{})
	}
}

// resume clears the paused state and wakes every parked waiter by closing the
// broadcast channel. It also zeroes the shared error counter so the resumed
// pool starts from a clean window. resume is the ONLY exit from a circuit
// break — there is no self-heal. Idempotent: resuming a running breaker is a
// no-op.
func (c *circuitBreaker) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecErrors = 0
	if !c.paused {
		return
	}
	c.paused = false
	c.reason = ""
	c.pausedAt = time.Time{}
	if c.resumed != nil {
		close(c.resumed)
		c.resumed = nil
	}
}

// status returns a snapshot of the breaker's paused state for surfacing to
// operators (pipeline_status, staleness footer).
func (c *circuitBreaker) status() circuitStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return circuitStatus{
		Paused: c.paused,
		Reason: c.reason,
		Since:  c.pausedAt,
	}
}
