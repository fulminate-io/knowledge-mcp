// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_heal_breaker.go is the per-(graphType, name) auto-heal circuit breaker and
// the strict no-progress/progress classifier for the two AUTO heal triggers (the
// embed-drain buildHealFactory closure and the periodic reconcileSegmentCoverage).
// It lives in bootstrap because everything the classifier reads — the segment
// manager and the RebuildSegments result counts — is bootstrap-local; pipeline/ has
// none of it.
//
// The breaker mirrors the pipeline/circuit_breaker.go latch idiom (latch-after-K, NO
// self-heal, NO auto-probe) but is NOT reused: that breaker is ErrClass/LLM-coupled
// and axis-scoped, while this one keys per graph and trips on heal no-progress. The
// two are deliberately independent.
//
// PROGRESS IS JUDGED BY THE SCANNED/PARTIAL COUNTS, and by nothing else. It used to
// have a second judge — a re-probe of shipped completeness against a fresh server
// manifest — and that judge is gone with the manifest. The remaining rule is
// correspondingly coarser and is stated plainly rather than implied: a rebuild that
// scans nodes counts as progress even when it ships a content-hash no-op.
//
// The engine distinction that once motivated the split no longer holds either. A
// rebuild used to write a SECOND, deterministic engine and could never raise the READ
// engine's resident count; the reset now swaps its layer into the very engine the
// read observes, so read-engine coverage IS movable by a rebuild.

// healBreakerTripThreshold is the number of CONSECUTIVE no-progress heal passes that
// latches a graph's auto-heal disarmed. It is the ONLY no-progress bound left in this
// package — it was once named independently of a publish-retry bound in segmentdist,
// and that bound died with the publish. This breaker LATCHES until a manual
// rebuild_segments op or a restart clears it; there is no self-heal.
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
// (a publish-suppression stamp on the segment manager).
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
//   - scanned>0 → PROGRESS, judged ONLY by scanned. A real scan is progress.
//     Everything else — explicitly including a rebuild that built nothing and shipped
//     only a sub-1024 tail, which is a real ship — is PROGRESS.
//
// THE SHIPPED-COMPLETENESS SUB-CASE IS GONE, not merely unreachable. It re-probed a
// FRESH server manifest snapshot to ask whether shipped completeness had improved;
// with no server manifest there is nothing to re-probe against, and the local
// alternative would compare the L2 cache against itself. The classifier is
// correspondingly coarser: a rebuild that scans nodes but ships a content-hash
// no-op now reads as PROGRESS and does not advance the breaker toward its latch.
//
// IT NO LONGER TAKES THE built AND partial COUNTS. They were the operands of exactly
// that departed sub-case, and once it went they were passed by every caller and read
// by none — a signature promising a judgement this function does not make. The counts
// still exist on the RebuildSegments outcome the callers hold; nothing here consumes
// them. Re-introducing a completeness rule means re-introducing the parameters WITH
// the code that reads them, never a parameter on its own.
func (c *client) classifyHealOutcome(
	gt kgtypes.GraphType, name string, ran bool, scanned int,
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
	// scanned>0 — a real scan is progress.
	c.healBreaker.RecordProgress(gt, name)
}
