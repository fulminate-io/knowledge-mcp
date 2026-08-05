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

// incompletePublishWarnStreak is the consecutive-agent-409 count at which
// markIncompletePublish escalates its per-cycle transient skip WARN to a LOUD
// persistent-degradation WARN. Below it a 409 is expected to self-heal: the handler
// un-stamps the missing ids so the next ship diff re-uploads them, and a healthy heal
// clears within a cycle or two. At or past it the re-upload is demonstrably not
// sticking (the agent keeps reporting the same blobs absent), which is a genuine wedge
// an operator must SEE rather than a best-effort WARN that scrolls past as noise. It is
// a log-escalation threshold ONLY — it never suppresses the retry (the 409 cause, unlike
// the coverage-ratio skip, keeps needing to retry until the re-upload lands).
const incompletePublishWarnStreak = 3

// This file holds the lifecycle-aware publish-gate predicates the embed ship
// points (ReEmitDirtyBuckets/Flush) consult before running a ship/publish
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

// completedSwapCount is the shipMu-guarded read of the landed-manifest-swap
// counter. Callers read it BEFORE and AFTER a ship/publish call: a rise means
// PublishManifest succeeded and the writer's manifest was replaced, while an
// unchanged count means the publish was skipped (coverage gate, agent 409) even
// though the call returned a nil error.
func (m *distManager[Q, S]) completedSwapCount() uint64 {
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	return m.completedSwaps
}

// setPublishPending latches the publishPending retry bit under shipMu. Called from
// publishResident's TRANSIENT non-success outcome points (coverage-read List error,
// transport error) — causes that must retry indefinitely because they self-clear once
// the transient condition passes. Two non-success causes route elsewhere: the
// coverage-ratio skip goes through markCoverageSkip (which BOUNDS the re-arm), and the
// agent-409 incomplete skip goes through markIncompletePublish (which un-stamps the
// missing ids and escalates the WARN). The success path clears the bit under the
// reconcile lock it already holds.
func (m *distManager[Q, S]) setPublishPending() {
	m.shipMu.Lock()
	m.publishPending = true
	m.shipMu.Unlock()
}

// markIncompletePublish handles the agent-409 (manifestIncompleteError) publish skip:
// the agent HEAD-verify reported one or more referenced blobs genuinely absent
// server-side. It does three things under one shipMu acquire:
//
//   - UN-STAMPS every missing id from BOTH shippedIDs and locallyShipped. This is the
//     convergence fix: shipAndPublish's ship diff SKIPS every id already in shippedIDs,
//     so a stamped-but-absent-server-side blob is otherwise never re-uploaded and the
//     409 recurs forever (the permanent wedge). Dropping the id from both views —
//     symmetric with shipNew, which re-stamps both on the re-ship — puts it back in the
//     next ship diff, so the next tick re-uploads it and the following publish
//     references a blob the server now holds.
//   - ARMS the retry bit UNCONDITIONALLY. Unlike markCoverageSkip (which clears the bit
//     once a cause that cannot self-clear passes its bound), the 409 cause IS supposed
//     to self-heal via the re-upload above, so the retry must stay armed until it lands.
//   - COUNTS consecutive 409s and, at incompletePublishWarnStreak, ESCALATES the
//     per-cycle transient skip WARN to a loud persistent-degradation WARN — the
//     difference between a 409 healing within a cycle and one whose re-upload is not
//     sticking. The loud WARN keeps firing every cycle while the wedge persists; a
//     landed swap resets the streak (publishResident) so a later 409 re-arms fresh.
//
// The WARNs are emitted OUTSIDE shipMu (they format the target identity, which needs no
// lock), mirroring markCoverageSkip's terminal WARN.
func (m *distManager[Q, S]) markIncompletePublish(missing []string) {
	m.shipMu.Lock()
	for _, id := range missing {
		delete(m.shippedIDs, id)
		delete(m.locallyShipped, id)
	}
	m.publishPending = true
	m.incompletePublishStreak++
	streak := m.incompletePublishStreak
	m.shipMu.Unlock()

	if streak >= incompletePublishWarnStreak {
		slog.Warn("segmentdist: publish PERSISTENTLY incomplete — re-uploads are not converging, corpus is degrading (agent keeps reporting missing blob(s))",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "missing", missing, "consecutive_skips", streak)
		return
	}
	slog.Warn("segmentdist: publish SKIPPED (agent reported missing blob(s) — un-stamped for re-upload, manifest+blobs left intact)",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "missing", missing, "consecutive_skips", streak)
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
