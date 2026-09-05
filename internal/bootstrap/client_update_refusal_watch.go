// SPDX-License-Identifier: Apache-2.0

// client_update_refusal_watch.go — react to a cloud client-version refusal by
// looking for an upgrade sooner, instead of waiting out the daily interval.
//
// IT IS A SEPARATE FILE FOR ONE REASON THAT MATTERS: it is the only file in
// this feature that imports the client-version package, so everything else here
// stays compilable and shippable independently of that package's own work.
//
// THIS IS A POLL AND IT IS NAMED ONE. The version package's transports LATCH a
// refusal; nothing there exposes a channel, a callback or a subscribe function,
// and adding one would be changing another ticket's package surface
// unilaterally. So this side does not get woken — it LOOKS. The read is cheap
// by construction: the latch is a package-level struct behind a read-write
// mutex, so a tick costs one uncontended RLock, no I/O and no allocation.
//
// WHAT THIS DOES NOT DO: it re-decides no guard, adds no second install
// decision path, and treats a cloud refusal as authority to bypass nothing. A
// refusal is a reason to LOOK SOONER. Nothing more.

package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// updateRefusalWatchInterval is how often the watcher reads the refusal latch.
//
// THIRTY SECONDS is chosen against two facts. The cost side: one uncontended
// RLock on a package-level struct per tick, which is free at this cadence. The
// benefit side: it bounds the reaction to a refusal at well under a minute
// against a scheduled check interval of a DAY, which is what "act immediately
// rather than waiting for the next tick" actually requires. A shorter period
// would buy nothing an operator could perceive; a longer one starts to look
// like the wait it exists to remove.
const updateRefusalWatchInterval = 30 * time.Second

// updateTriggeredCheckMinInterval is the minimum spacing between consecutive
// REFUSAL-TRIGGERED checks.
//
// A refusal is not a single event — every routed cloud operation hits it — so
// an uncoalesced trigger would turn one server-side policy change into a burst
// of release-endpoint requests from every affected daemon at once. This bounds
// that, and it is the second of TWO suppressors: this one expires, while the
// per-refusal de-duplication below does not.
const updateTriggeredCheckMinInterval = 15 * time.Minute

// updateCheckNow is the clock every interval comparison in this file reads.
//
// It is an overridable package var, mirroring the transcript loop's own seam,
// and it is MANDATORY rather than a convenience. Two mechanisms here suppress a
// repeat check by different means: the per-refusal de-duplication suppresses
// forever for an already-handled latch, and updateTriggeredCheckMinInterval
// suppresses only until it elapses. An implementation carrying only the
// interval looks correct on any test that triggers twice in quick succession,
// because the interval alone swallows the second call — the two are
// indistinguishable unless a test can advance PAST the interval. This seam is
// what makes that possible, and therefore what makes the convergence property
// falsifiable rather than merely asserted.
//
//nolint:gochecknoglobals // overridable clock seam for testability.
var updateCheckNow = time.Now

// refusalWatchState is the watcher's bookkeeping: which refusal it last acted
// on, when it last ran a triggered check, and whether one is in flight.
type refusalWatchState struct {
	mu sync.Mutex
	// lastActedAt is the At of the refusal most recently acted on. ACTING ONCE
	// PER DISTINCT REFUSAL is not an optimization, it is the convergence
	// property: without it a client that is PERMANENTLY refused — a dev build,
	// which the dev-stamp guard will refuse to upgrade forever — would drive a
	// check every minimum interval for the life of the process, which is a lane
	// firing forever on one cause. With it, such a client performs exactly one
	// no-op check per new latch and then goes quiet.
	lastActedAt time.Time
	// lastTriggeredAt is when the last triggered check ran.
	lastTriggeredAt time.Time
	// inFlight collapses concurrent or rapid triggers to a single check.
	inFlight bool
}

// shouldTrigger decides whether this observation warrants a check, and claims
// the in-flight slot when it does.
//
// IT DOES NOT LOOK AT Reason. Every reason drives a check, whatever the string
// is; the update tick's own guards then decide whether anything is installed.
// That is what keeps this coupling safe across a repo boundary: this side holds
// no copy of the other's reason vocabulary in code, so that vocabulary can grow,
// shrink or be reordered with no change here. The wrong-but-compiling
// implementation this rules out is ANY switch on Reason — the narrow
// single-value form silently ignores every other reason including the
// absent-header case, which is the client too old to send the header at all and
// therefore the population most in need of the upgrade, and the exhaustive form
// silently ignores whatever reason is added next.
func (s *refusalWatchState) shouldTrigger(r clientver.Refusal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight {
		return false
	}
	if !r.At.After(s.lastActedAt) {
		return false
	}
	now := updateCheckNow()
	if !s.lastTriggeredAt.IsZero() && now.Sub(s.lastTriggeredAt) < updateTriggeredCheckMinInterval {
		return false
	}
	s.lastActedAt = r.At
	s.lastTriggeredAt = now
	s.inFlight = true
	return true
}

// done releases the in-flight slot.
func (s *refusalWatchState) done() {
	s.mu.Lock()
	s.inFlight = false
	s.mu.Unlock()
}

// runUpdateRefusalWatch polls the refusal latch and drives ONE guarded check per
// distinct refusal.
//
// The check goes through the SAME per-tick outcome helper the boot-delay
// one-shot and the ticker use, so all six guards apply unchanged and no second
// decision path exists.
func (c *client) runUpdateRefusalWatch(ctx context.Context, f Config) {
	state := &refusalWatchState{}
	ticker := time.NewTicker(updateRefusalWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkOnRefusal(ctx, f, state)
		}
	}
}

// checkOnRefusal reads the latch once and runs a triggered check if this
// observation warrants one. Split out so a test can drive one observation
// without waiting out a ticker.
func (c *client) checkOnRefusal(ctx context.Context, f Config, state *refusalWatchState) {
	r, ok := clientver.CurrentRefusal()
	if !ok {
		return
	}
	if !state.shouldTrigger(r) {
		return
	}
	defer state.done()
	slog.Info("update check: triggered by a cloud client-version refusal",
		"reason", r.Reason, "minimum", r.Minimum, "client_version", r.ClientVersion)
	// The refusal's own fields ride into the health snapshot so an operator
	// reading the status surface sees WHY the daemon acted and what the cloud
	// demanded. The Reason is recorded VERBATIM rather than mapped through a
	// known-member table: an unrecognized reason must reach the operator
	// intact, since that is the only way they learn about a reason shipped
	// after this binary was built.
	c.recordRefusalOrigin(r)
	c.logUpdateCheckOutcome(ctx, f)
}

// recordRefusalOrigin stamps the triggering refusal onto the health snapshot.
func (c *client) recordRefusalOrigin(r clientver.Refusal) {
	if c.updateHealth == nil {
		return
	}
	c.updateHealth.RecordRefusalOrigin(r.Reason, r.Minimum, r.ClientVersion, updateCheckNow())
}
