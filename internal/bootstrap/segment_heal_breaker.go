// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// segment_heal_breaker.go is the per-(graphType, name) auto-heal circuit breaker and
// the strict no-progress/progress classifier for the two AUTO heal triggers (the
// embed-drain buildHealFactory closure and the periodic reconcileSegmentCoverage).
// It lives in bootstrap because everything the classifier reads — the segment
// manager's shipped-completeness probes, IsL2Authoritative, and the RebuildSegments
// result counts — is bootstrap-local; pipeline/ has none of it.
//
// The breaker mirrors the pipeline/circuit_breaker.go latch idiom (latch-after-K, NO
// self-heal, NO auto-probe) but is NOT reused: that breaker is ErrClass/LLM-coupled
// and axis-scoped, while this one keys per graph and trips on heal no-progress. The
// two are deliberately independent.
//
// ENGINE DISTINCTION (governs every progress judgment here): a from-scratch PG
// RebuildSegments writes the DETERMINISTIC engine and ships to the server; it can
// NEVER raise the READ engine's resident doc count. So progress is judged ONLY by
// shipped-completeness (a fresh ShippedManifestSnapshot re-probe) or the
// scanned/partial counts — NEVER by read-engine coverage, which a rebuild cannot move.

// healBreakerTripThreshold is the number of CONSECUTIVE no-progress heal passes that
// latches a graph's auto-heal disarmed. It is separate from and named independently
// of segmentdist.coverageSkipMaxStreak (the publish-retry bound) — different mechanism
// (rebuild trigger vs publish retry), different reset policy: this breaker LATCHES
// until a manual rebuild_segments op or a restart clears it (no self-heal), whereas
// the publish bound auto-re-arms on a resident rise.
const healBreakerTripThreshold = 2

// The disarm sentinel the trigger-1 (embed-drain) heal closure returns when the
// breaker has latched this graph disarmed lives in the pipeline package
// (pipeline.ErrHealDisarmed) — NOT here — because the CONSUMER, maybeHealCheck, is in
// pipeline and the import direction is bootstrap→pipeline (pipeline cannot import
// bootstrap). The closure in client_segment.go returns pipeline.ErrHealDisarmed;
// maybeHealCheck errors.Is-matches it to set the collector's healDisarmed flag without
// WARNing (a disarm is not a failure).

// segmentHealBreaker is the per-(graphType, name) heal latch map. A zero value is
// usable (the map is lazily allocated under mu), so a test-built *client that never
// runs constructClient participates without extra wiring. All state is guarded by mu.
type segmentHealBreaker struct {
	mu    sync.Mutex
	state map[string]*healBreakerEntry
}

// healBreakerEntry is one graph's breaker state: the consecutive no-progress streak,
// whether the breaker has latched disarmed, and WHEN it latched. Once latched, only
// ClearHealLatch (a manual rebuild_segments success) or a process restart clears it.
//
// latchedAtNanos is a wall-clock time.Now().UnixNano() stamped on the one call that
// latches and zeroed with the rest of the entry on re-arm, so it is non-zero exactly
// while latched is true. It is PER-PROCESS state like the latch it accompanies — a
// restart clears both — so an age derived from it measures how long auto-heal has
// been disarmed IN THIS PROCESS, which is the same lifetime the latch itself has.
type healBreakerEntry struct {
	noProgressStreak int
	latched          bool
	latchedAtNanos   int64
}

// healBreakerKey keys the latch map on (graphType, name), matching the RebuildSegments
// single-flight key shape so a custom graph and a code graph of the same name never
// collide.
func healBreakerKey(gt kgtypes.GraphType, name string) string {
	return string(gt) + "/" + name
}

// entryLocked returns the entry for (gt, name), lazily allocating the map + entry.
// Caller holds b.mu.
func (b *segmentHealBreaker) entryLocked(gt kgtypes.GraphType, name string) *healBreakerEntry {
	if b.state == nil {
		b.state = map[string]*healBreakerEntry{}
	}
	key := healBreakerKey(gt, name)
	e := b.state[key]
	if e == nil {
		e = &healBreakerEntry{}
		b.state[key] = e
	}
	return e
}

// Allow reports whether an auto-heal RebuildSegments may fire for (gt, name). It is
// false once the breaker has latched this graph disarmed after healBreakerTripThreshold
// consecutive no-progress passes. The two AUTO triggers consult it BEFORE RebuildSegments.
func (b *segmentHealBreaker) Allow(gt kgtypes.GraphType, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.entryLocked(gt, name).latched
}

// RecordNoProgress advances the consecutive no-progress streak for (gt, name) and
// trips (latches disarmed) at healBreakerTripThreshold. It returns tripped == true
// EXACTLY on the call that latches, and emits the terminal WARN then. A no-progress
// pass on an already-latched breaker is a no-op (no self-heal, no re-trip).
func (b *segmentHealBreaker) RecordNoProgress(gt kgtypes.GraphType, name string) (tripped bool) {
	b.mu.Lock()
	e := b.entryLocked(gt, name)
	if e.latched {
		b.mu.Unlock()
		return false
	}
	e.noProgressStreak++
	if e.noProgressStreak >= healBreakerTripThreshold {
		e.latched = true
		e.latchedAtNanos = time.Now().UnixNano()
		b.mu.Unlock()
		slog.Warn("bootstrap: auto-heal suspended for graph until a manual rebuild_segments or restart (heal breaker latched after consecutive no-progress rebuilds)",
			"graph_type", gt, "name", name, "no_progress_streak", healBreakerTripThreshold)
		return true
	}
	b.mu.Unlock()
	return false
}

// RecordProgress resets the consecutive no-progress streak for (gt, name) after a
// heal pass that made genuine progress (per the 2.1 classification). It does NOT
// un-latch a tripped breaker — once latched there is NO self-heal; only ClearHealLatch
// or a restart clears it. So a RecordProgress on a latched breaker is a no-op.
func (b *segmentHealBreaker) RecordProgress(gt kgtypes.GraphType, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entryLocked(gt, name)
	if e.latched {
		return
	}
	e.noProgressStreak = 0
}

// ClearHealLatch clears the latch AND the streak for (gt, name) — the manual-op /
// restart re-arm. It is exposed to the tools layer via the ClientDeps seam and called
// from handleClientRebuildSegments' success branch (keyed on scanned>0): an operator
// asking for a rebuild that actually scanned nodes re-arms the auto-heal so the manual
// intervention resumes the automatic path. Clearing a non-latched breaker is harmless.
func (b *segmentHealBreaker) ClearHealLatch(gt kgtypes.GraphType, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entryLocked(gt, name)
	e.latched = false
	e.noProgressStreak = 0
	e.latchedAtNanos = 0
}

// LatchedSince reports the wall-clock nanos at which (gt, name) latched disarmed, and
// 0 when the breaker is not latched — so a caller can render how long a graph has been
// stalled without a second membership question. It is the stall stamp's heal-breaker
// half; the publish coverage gate owns the other half
// (segmentdist.Manager.CoverageSuppressedSince).
//
// It reads the map WITHOUT the lazy entryLocked allocation Allow uses, deliberately:
// its caller is the manage(status) coverage table, which asks about every eligible
// graph rather than only the ones a heal pass has touched, and a graph the breaker has
// never recorded is by definition not latched.
func (b *segmentHealBreaker) LatchedSince(gt kgtypes.GraphType, name string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.state[healBreakerKey(gt, name)]
	if e == nil || !e.latched {
		return 0
	}
	return e.latchedAtNanos
}

// classifyHealOutcome is the STRICT v5 no-progress/progress classifier for one AUTO
// RebuildSegments outcome. It records against the breaker ONLY when ran==true (a
// ran==false coalesce or nil-deps "pipeline not wired" is never recorded). The rules:
//
//   - scanned==0 → NO-PROGRESS (loud terminal WARN + RecordNoProgress). This is the
//     live loop's signal: a rebuild that scans zero nodes with retrievable vectors
//     ships nothing and can never raise coverage.
//   - scanned>0 on the OSS/L2 path (IsL2Authoritative) → PROGRESS, judged ONLY by
//     scanned (no completeness sub-case: L2 snapshots stamp DocCount=0, so the
//     completeness probe would anyUnknown-disarm anyway).
//   - scanned>0 on the CLOUD path → PROGRESS unless the pure-read post-pass re-probe
//     (HasShippedFromSnapshot + segmentPoolDegenerate over a FRESH ShippedManifestSnapshot)
//     shows shipped-completeness did NOT improve, in which case NO-PROGRESS. Everything
//     else — explicitly including scanned>0 && built==0 && partial>0 (a sub-1024
//     tail-only rebuild is a real ship) — is PROGRESS.
//
// The re-probe is DELIBERATELY the two pure reads (HasShippedFromSnapshot +
// segmentPoolDegenerate over a fresh snapshot) — NEVER a wholesale healNeedsRebuild
// call, which re-runs ReconcileResidentDegenerate → load()+recoverIfDegenerate and
// mutates the very recovery state the breaker accounts over.
func (c *client) classifyHealOutcome(
	ctx context.Context, gt kgtypes.GraphType, name string, ran bool, scanned, built, partial int,
) {
	if !ran {
		// ran==false is a benign coalesce (another rebuild already in flight) or a
		// nil-deps "pipeline not wired" no-op — neither is a heal outcome, so never record.
		return
	}
	if scanned == 0 {
		slog.Warn("bootstrap: auto-heal ran but scanned 0 nodes with retrievable vectors — no-progress heal (shipped nothing, coverage cannot recover)",
			"graph_type", gt, "name", name)
		c.healBreaker.RecordNoProgress(gt, name)
		return
	}
	// scanned>0. The OSS/L2 path judges ONLY by scanned — a real scan is progress.
	if c.segmentMgr.IsL2Authoritative(gt, name) {
		c.healBreaker.RecordProgress(gt, name)
		return
	}
	// Cloud path: a real scan is progress UNLESS the shipped-completeness re-probe shows
	// the pool is still absent/degenerate (the content-hash no-op re-ship case).
	if c.healCompletenessImproved(ctx, gt, name) {
		c.healBreaker.RecordProgress(gt, name)
		return
	}
	slog.Warn("bootstrap: auto-heal ran (scanned>0) but shipped completeness did not improve — no-progress heal",
		"graph_type", gt, "name", name, "scanned", scanned, "built", built, "partial", partial)
	c.healBreaker.RecordNoProgress(gt, name)
}

// healCompletenessImproved is the CLOUD-path post-pass re-probe: it reports whether
// the shipped corpus is now present AND non-degenerate over a FRESH ShippedManifestSnapshot.
// It uses ONLY the two pure reads (HasShippedFromSnapshot + segmentPoolDegenerate) —
// NEVER healNeedsRebuild, whose ReconcileResidentDegenerate leg would mutate recovery
// state. A probe error is treated CONSERVATIVELY as improved (progress): an inability
// to measure must not latch the breaker on a transient List failure.
func (c *client) healCompletenessImproved(ctx context.Context, gt kgtypes.GraphType, name string) bool {
	snapshot, err := c.segmentMgr.ShippedManifestSnapshot(ctx, gt, name, hnsw.New().Name())
	if err != nil {
		return true // conservative: cannot measure → do not trip.
	}
	if !c.segmentMgr.HasShippedFromSnapshot(snapshot) {
		return false // still nothing shipped — completeness did not improve.
	}
	degenerate, err := c.segmentPoolDegenerate(ctx, gt, name, snapshot)
	if err != nil {
		return true // conservative: probe error → do not trip.
	}
	return !degenerate // improved iff the pool is present AND covers enough.
}
