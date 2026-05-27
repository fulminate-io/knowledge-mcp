// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// errBackoff is a shared exponential-backoff gate guarding LLM calls across
// every summary + embed worker. The resource it protects — the provider /
// subscription rate limit — is global, so ONE gate is shared rather than one
// per graph or per worker.
//
// Mechanics: a worker calls wait(ctx) before each LLM batch call. A transient
// failure (rate limit / overload, as classified by llm.IsTransient) calls
// fail(), extending a global block window by base*2^(n-1) capped at maxDelay,
// with ±20% jitter to de-synchronize the worker pool's next attempt. A
// success calls ok(), resetting the consecutive-failure count and clearing
// the window. Terminal failures do NOT call fail() — they get a failure
// marker and are dropped, never retried, so they must not throttle healthy
// work.
//
// Why this exists: without the gate a transient rate limit caused every
// worker to re-attempt its batch on the next 250ms collector tick, a tight
// retry storm that filled a 3 GB client log in minutes (May 2026 incident).
type errBackoff struct {
	base     time.Duration
	maxDelay time.Duration

	mu           sync.Mutex
	consecutive  int
	blockedUntil time.Time
	rng          *rand.Rand
}

// newErrBackoff constructs the gate. base is the first-failure delay;
// maxDelay caps the exponential growth. Defaults applied for non-positive
// or inverted inputs so a zero-value Config never produces a degenerate gate.
func newErrBackoff(base, maxDelay time.Duration) *errBackoff {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if maxDelay < base {
		maxDelay = 60 * time.Second
	}
	return &errBackoff{
		base:     base,
		maxDelay: maxDelay,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// wait blocks until the current backoff window elapses or ctx is canceled.
// Returns immediately in steady state (no window open).
func (b *errBackoff) wait(ctx context.Context) {
	b.mu.Lock()
	d := time.Until(b.blockedUntil)
	b.mu.Unlock()
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ok resets the gate after a successful LLM call. Cheap no-op when already
// at zero (the steady-state hot path).
func (b *errBackoff) ok() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.consecutive == 0 {
		return
	}
	b.consecutive = 0
	b.blockedUntil = time.Time{}
}

// fail extends the backoff window after a transient LLM failure and returns
// the delay applied (for logging). Concurrent fails from the worker pool
// take the max window rather than stacking — N simultaneous rate-limit
// errors should open ONE window, not N sequential ones.
func (b *errBackoff) fail() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive++
	// base * 2^(consecutive-1), saturating at maxDelay. Shift is capped so
	// the exponent never overflows the int64 nanosecond range.
	shift := min(b.consecutive-1, 32)
	d := b.base << shift
	if d <= 0 || d > b.maxDelay {
		d = b.maxDelay
	}
	// ±20% jitter so the worker pool doesn't wake in lockstep.
	if span := int64(d) / 5; span > 0 {
		d += time.Duration(b.rng.Int63n(2*span+1) - span)
	}
	if until := time.Now().Add(d); until.After(b.blockedUntil) {
		b.blockedUntil = until
	}
	return d
}
