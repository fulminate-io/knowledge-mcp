// SPDX-License-Identifier: Apache-2.0

// client_update_health.go — the update checker's health tracker and the
// accessor the manage(status) surface reads it through.

package bootstrap

import (
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// updateHealthTracker accumulates the background update checker's outcomes.
//
// Concurrency: every field is guarded by mu. The loop and the boot-delay
// one-shot are two goroutines that both Record, and the status surface reads
// concurrently with either.
type updateHealthTracker struct {
	mu sync.Mutex
	h  tools.UpdateHealth
}

func newUpdateHealthTracker() *updateHealthTracker { return &updateHealthTracker{} }

// Record folds one tick's outcome into the running health and returns the
// resulting snapshot, so the caller's log branch reads the SAME numbers the
// status surface will render.
//
// The failure streak advances on a failed tick and resets on any tick that did
// not fail — including a refusal, because a guard that says "brew owns this"
// is a correct outcome, not a failure, and letting it advance a failure streak
// would escalate a healthy daemon's log to Error for doing the right thing.
func (t *updateHealthTracker) Record(out updateCheckOutcome, now time.Time) tools.UpdateHealth {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.h.TotalChecks++
	t.h.LastCheck = now
	if out.Tag != "" {
		t.h.LatestKnown = out.Tag
	}

	switch {
	case out.Err != nil:
		t.h.ConsecutiveFailures++
		t.h.LastFailure = now
		t.h.LastError = out.Err.Error()
	default:
		t.h.ConsecutiveFailures = 0
		t.h.LastError = ""
	}

	if out.Installed && out.Err == nil {
		t.h.LastInstall = now
		t.h.InstalledVersion = out.Tag
	}
	// The no-action reason is REPLACED every tick, including with the empty
	// string when a tick acted or failed. It describes the LAST tick, not a
	// high-water mark, so a stale "brew-managed" left standing after the
	// condition cleared would misreport why the updater is idle.
	t.h.NoActionReason = string(out.Skipped)
	return t.h
}

// RecordRefusalOrigin stamps the cloud client-version refusal that drove the
// check about to run, so the status surface can explain an out-of-band check
// rather than showing an unexplained one.
//
// The reason is stored VERBATIM as received. Mapping it through a table of
// known values would silently blank a reason shipped after this binary was
// built, which is exactly the operator this field exists for.
func (t *updateHealthTracker) RecordRefusalOrigin(reason, minimum, clientVersion string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.h.TriggerReason = reason
	t.h.TriggerMinimum = minimum
	t.h.TriggerClientVersion = clientVersion
	t.h.TriggeredAt = now
}

// Snapshot returns the current health.
func (t *updateHealthTracker) Snapshot() tools.UpdateHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.h
}

// UpdateCheckHealth satisfies the optional interface the manage(status) surface
// reads, so an operator can see a persistently failing — or a deliberately
// idle — updater rather than silence.
//
// ok=false when the tracker is nil: a test-built client, or a daemon whose
// disable gate refused so the loops were never spawned. Callers render that as
// ABSENT rather than as a healthy zero snapshot, mirroring the transcript
// accessor.
func (c *client) UpdateCheckHealth() (tools.UpdateHealth, bool) {
	if c.updateHealth == nil {
		return tools.UpdateHealth{}, false
	}
	return c.updateHealth.Snapshot(), true
}
