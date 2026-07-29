// SPDX-License-Identifier: Apache-2.0

package segmentdist

import "log/slog"

// coverageSkipMaxStreak bounds the publishPending re-arm on the ONE cause that can
// never self-clear by retrying: the coverage-ratio skip. When the read engine's
// resident doc count sits below the shipped-corpus ratio, publishResident skips the
// publish and (pre-bound) re-armed publishPending every drain — a self-sustaining
// re-fire, because a re-attempt reads the SAME sub-ratio resident and skips again.
// After this many consecutive coverage-skips at a non-rising resident, markCoverageSkip
// STOPS re-arming (terminal WARN) until the resident actually rises. It is separate
// from and named independently of the bootstrap heal breaker's healBreakerTripThreshold
// — different mechanism (publish retry vs rebuild trigger), different reset policy
// (this auto-re-arms on a resident rise; the breaker latches until a manual op/restart).
const coverageSkipMaxStreak = 2

// This file holds the lifecycle-aware publish-gate predicates the embed entry
// points (AddAndShip/AddAndShipFields/Flush) consult before running a ship/publish
// pass. The gate skips a no-progress publish for sub-threshold unsealed batches:
// ship/publish runs iff a SEALED unshipped export exists (hasUnshippedExport) OR a
// prior publish did not land and is pending retry (publishRetryPending). The retry
// bit itself is set/cleared inside publishResident (manager_prune.go), the one site
// that sees every publish outcome.

// hasUnshippedExport reports whether a SEALED unshipped export exists: there is at
// least one exported segment whose id is not yet in shippedIDs (or the seed has not
// yet run). It is a read-only projection of the ship-new diff loop in shipAndPublish
// — same shippedIDs-membership test, returning a bool instead of building the diff.
// Export() is read OUTSIDE shipMu (exactly as shipAndPublish does) because Export
// takes its own engine lock; the shippedIDs membership walk then takes shipMu.
func (m *distManager[Q, S]) hasUnshippedExport() bool {
	exported := m.engine.Export()
	if len(exported) == 0 {
		return false
	}
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	if !m.seeded {
		return true
	}
	for _, b := range exported {
		if _, sent := m.shippedIDs[b.ID]; !sent {
			return true
		}
	}
	return false
}

// publishRetryPending is the shipMu-guarded read of the publishPending retry bit
// (mirrors the seeded read in ensureShippedSeeded). The embed gate ORs it with
// hasUnshippedExport so a shipped-but-unpublished set re-attempts the publish.
func (m *distManager[Q, S]) publishRetryPending() bool {
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	return m.publishPending
}

// setPublishPending latches the publishPending retry bit under shipMu. Called from
// publishResident's TRANSIENT non-success outcome points (coverage-read List error,
// 409 skip, transport error) — causes that must retry indefinitely because they
// self-clear once the transient condition passes. The coverage-ratio skip does NOT
// use this; it goes through markCoverageSkip, which bounds the re-arm. The success
// path clears the bit under the reconcile lock it already holds.
func (m *distManager[Q, S]) setPublishPending() {
	m.shipMu.Lock()
	m.publishPending = true
	m.shipMu.Unlock()
}

// markCoverageSkip is the CAUSE-SCOPED, PROGRESS-GATED publish-retry bound for the
// coverage-ratio skip — the one non-success cause that a retry cannot clear on its
// own (a re-attempt reads the SAME sub-ratio resident and skips again, so the
// unbounded setPublishPending re-armed the publish every drain forever: the
// self-sustaining page-read loop). It replaces the setPublishPending call at the
// coverage-ratio-skip branch (publishResident).
//
// Semantics: it counts consecutive coverage skips at a NON-RISING resident and stops
// re-arming publishPending once the streak passes coverageSkipMaxStreak, emitting a
// terminal WARN. A resident RISE (genuine progress toward coverage) resets the streak
// so a healing engine re-arms — the deliberate asymmetry with the bootstrap heal
// breaker (which latches until a manual op/restart). Suppression only stops re-ARMING
// while the read engine is stuck; a later resident-rise re-arm still below ratio
// re-skips harmlessly and the publish lands only once the ratio passes.
//
// It also fires the onCoverageSuppressed hook on the TRANSITION into suppression —
// the moment retrying stops being able to help, which is the useful moment to ask a
// periodic reconcile consumer to look sooner. It only RECORDS: no rebuild, and
// nothing that drives one, runs from this path, so rebuild ownership stays with the
// consumer and the existing single-flight remains the only rebuild entry point.
//
// Locking: ResidentDocCount() is read FIRST, OUTSIDE shipMu (it takes the engine's
// own lock via the resident set — a DIFFERENT lock, so no nesting), then the streak
// bookkeeping runs under ONE shipMu acquire. The publishPending write is done INLINE
// under that same acquire (calling setPublishPending here would re-acquire shipMu and
// self-deadlock). The terminal WARN and the suppression hook are both emitted OUTSIDE
// the lock — the hook takes the owner's nudge lock, and acquiring a second lock while
// holding shipMu would put a lock-order dependency on the publish path.
func (m *distManager[Q, S]) markCoverageSkip() {
	resident := m.engine.ResidentDocCount()

	m.shipMu.Lock()
	if resident > m.lastSkipResident {
		// Genuine progress since the last skip — the engine is climbing toward
		// coverage. Reset the streak so a recovering engine re-arms.
		m.coverageSkipStreak = 0
		m.lastSkipResident = resident
	}
	m.coverageSkipStreak++
	suppress := m.coverageSkipStreak > coverageSkipMaxStreak
	// The EDGE into suppression, as distinct from suppress — which is true on every
	// skip past the bound. The streak equals coverageSkipMaxStreak+1 only on the FIRST
	// suppressing skip of an episode: a resident rise resets it to 0 just above and it
	// is then incremented to 1, so the threshold is crossed exactly once per episode.
	// That makes the hook below naturally debounced — one stuck episode fires it once
	// per engine, and the streak cannot re-reach the threshold until a rise resets it.
	transition := m.coverageSkipStreak == coverageSkipMaxStreak+1
	// Arm the retry while NOT suppressing; CLEAR it once suppressing. Clearing is
	// load-bearing: on the streaks below the bound the bit was latched true, so merely
	// "not re-arming" would leave it set and the gate would keep re-firing forever. On
	// the transition to suppress the bit goes false, which is what actually stops the
	// per-drain publish-retry re-fire until a resident rise re-arms it.
	m.publishPending = !suppress
	m.shipMu.Unlock()

	if suppress {
		slog.Warn("segmentdist: coverage gate unsatisfiable — suspending coverage-skip republish until resident rises",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "resident", resident)
	}
	// Outside shipMu (see the Locking note): record the graph so a periodic reconcile
	// consumer looks sooner than its own cadence. nil for a distManager built without
	// an owner.
	if transition && m.onCoverageSuppressed != nil {
		m.onCoverageSuppressed()
	}
}
