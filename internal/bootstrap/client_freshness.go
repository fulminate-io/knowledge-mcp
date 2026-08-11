// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"sync"
	"time"
)

// freshnessWakeCoolOff bounds how often account activity may wake the LLM
// pipeline. It is account-global rather than per-graph because the watermark it
// gates is itself account-scoped: one counter for everything the account owns,
// so there is nothing finer to key a window on.
const freshnessWakeCoolOff = 60 * time.Second

// freshnessTrigger is the per-client state behind the activity-driven pipeline
// wake: the last watermark this client observed, whether movement is still
// waiting to be drained, and when the last wake fired. The zero value is usable,
// so it lives on the client struct by value.
type freshnessTrigger struct {
	mu       sync.Mutex
	lastSeen uint64
	pending  bool
	lastWake time.Time
}

// checkPipelineFreshness is the activity hook: one cheap compare per tool call
// that turns "something in this account moved" into at most one pipeline wake
// per cool-off window.
//
// NO GOROUTINE, deliberately: the whole body is an atomic-backed read, a mutex
// compare and a non-blocking channel send — nanoseconds, off the RPC path.
// Spawning a goroutine would cost more than it saves.
//
// Accepted residual: movement suppressed inside the cool-off window fires on the
// next tool call, so if activity stops right after a suppressed move that wake
// never happens and the gap drains on the account's next activity. That is the
// designed semantics — every gap is born to an active writer, so draining on the
// writer's own next call covers the case that matters.
func (c *client) checkPipelineFreshness(ctx context.Context) {
	// The watermark's meaning changes with the backend: a value carried across a
	// login flip would be compared against a different account's counter. The
	// flip check has already signaled both pipeline wakes, so reset and stop.
	if c.pipeline != nil && c.pipeline.CheckLoginFlip(ctx) {
		c.freshness.reset()
		return
	}
	// Test harnesses build a client with no routing layer.
	if c.router == nil {
		return
	}
	if c.freshness.evaluate(c.router.FreshnessGen(ctx), time.Now()) {
		// Never called while holding the trigger's mutex.
		c.WakePipeline()
	}
}

// evaluate records the watermark this call observed and reports whether it
// earns a wake now. Split from checkPipelineFreshness so the cool-off window is
// exercisable against an injected clock rather than a real minute.
func (t *freshnessTrigger) evaluate(gen uint64, now time.Time) bool {
	// 0 is not a reading. The wire contract defines it as "the serving flavor
	// maintains no watermark or this replica has not loaded one yet" — a
	// non-value, never a counter to compare against.
	if gen == 0 {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// COMPARE FOR CHANGE, NEVER FOR INCREASE. The served value is a per-replica
	// sample on a short TTL, so it may move backward between replicas or after a
	// restart; the wire contract's own words: "A client must therefore treat ANY
	// CHANGE as movement and never test only for an increase."
	if gen != t.lastSeen {
		t.lastSeen = gen
		// pending is load-bearing and must not be simplified away: movement seen
		// INSIDE the cool-off window would otherwise be forgotten, so the last
		// write of a burst would never be drained.
		t.pending = true
	}

	if !t.pending || (!t.lastWake.IsZero() && now.Sub(t.lastWake) < freshnessWakeCoolOff) {
		return false
	}
	t.pending = false
	t.lastWake = now
	return true
}

// reset drops the observed watermark and the cool-off window, so the next
// observation is treated as a first sighting against the new backend.
func (t *freshnessTrigger) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSeen = 0
	t.pending = false
	t.lastWake = time.Time{}
}
