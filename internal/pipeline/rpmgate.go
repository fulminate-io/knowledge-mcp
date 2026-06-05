// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"time"
)

// rpmGate is a shared FIXED-RATE / LEAKY-BUCKET pacer that throttles embed
// dispatch to at most N requests per minute. It is a PROACTIVE throttle: a
// worker calls wait(ctx) before each embedder dispatch so the opening 20×100
// worker burst respects a low-tier Voyage account's RPM BEFORE the first 429
// lands — the reactive errBackoff (backoff.go) only opens a window AFTER a
// failure, so it cannot pace the opening burst. The two gates are distinct and
// composed (proactive pace, then reactive backoff) in processEmbedGroup.
//
// This is a LEAKY-BUCKET / fixed-rate pacer, NOT a token bucket: it does NOT
// accrue burst credit while idle. A long-idle gate admits exactly ONE request
// immediately and then paces the rest one interval apart — it never lets a
// backlog of "saved up" tokens fire at once. Do not "fix" this toward
// token-bucket burst semantics; strict burst-of-1 pacing is the point.
//
// RESERVE-THEN-WAIT INVARIANT (load-bearing — this is the whole reason the gate
// paces a CONCURRENT worker pool rather than nothing): on wait, the waiter must,
// UNDER THE LOCK, read lastGrant, compute target = max(now, lastGrant+interval),
// ADVANCE lastGrant to that target, THEN unlock, THEN sleep until target. The
// advance-under-lock-BEFORE-sleep reserves a STAGGERED slot per waiter, so N
// concurrent workers claim now+interval, now+2·interval, … and wake spread out.
// The naive alternative — compute target, unlock, sleep, advance AFTER sleeping
// — is WRONG: all N waiters read the same lastGrant, compute the same target,
// sleep the same duration, and wake together, pacing nothing. The concurrent
// sub-test in rpmgate_test.go exists specifically to catch that release-then-
// sleep regression.
//
// A non-positive rpm yields a DISABLED gate whose wait is a no-op (returns
// before taking the lock) — this preserves the current default (no rate cap)
// byte-for-byte and is the zero-overhead hot path when --embed-rpm is unset.
type rpmGate struct {
	interval time.Duration // 0 ⇒ disabled (no-op wait)

	mu        sync.Mutex
	lastGrant time.Time
}

// newRPMGate constructs the pacer. rpm is the requests-per-minute ceiling; an
// rpm <= 0 yields a disabled gate (wait is a no-op), preserving the default
// no-rate-cap behavior. The interval between successive grants is
// time.Minute / rpm.
func newRPMGate(rpm int) *rpmGate {
	if rpm <= 0 {
		return &rpmGate{}
	}
	return &rpmGate{interval: time.Minute / time.Duration(rpm)}
}

// wait blocks until this waiter's paced slot is reached or ctx is canceled.
// Disabled gate (interval == 0) returns immediately without taking the lock.
//
// Reserve-then-wait: under the lock it advances lastGrant to its reserved
// target (max(now, lastGrant+interval)) BEFORE releasing the lock, so
// concurrent waiters claim staggered slots rather than colliding on one. The
// sleep happens after the unlock and is ctx-cancelable.
func (g *rpmGate) wait(ctx context.Context) {
	if g.interval <= 0 {
		return
	}

	g.mu.Lock()
	// target = max(now, lastGrant+interval): the first grant (zero lastGrant)
	// resolves to now (admitted immediately); each subsequent grant is forced
	// at least one interval past the prior reservation. Advancing lastGrant to
	// target HERE, under the lock and before the sleep, is what staggers
	// concurrent waiters (see the RESERVE-THEN-WAIT note above).
	now := time.Now()
	target := g.lastGrant.Add(g.interval)
	if target.Before(now) {
		target = now
	}
	g.lastGrant = target
	g.mu.Unlock()

	d := time.Until(target)
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
