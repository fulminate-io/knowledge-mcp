// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// The BM25 arm of the auto-heal rebuild decision.
//
// healNeedsRebuildLocal is scoped to the HNSW format: it compares the HNSW resident
// doc count against the graph's embedded-node count and returns no-rebuild when it
// covers. A graph can have a healthy HNSW arm and a COLLAPSED BM25 arm at the same
// time — the two formats load from separately-rooted L2 caches — and on that shape
// the HNSW-scoped check short-circuits before the field corpus is ever examined, so
// the collapsed arm has no trigger to recover through. This file is the second arm
// of that decision, and healNeedsRebuild consults it whenever the HNSW arm is
// satisfied.
//
// THE SHAPE THIS EXISTS FOR IS NOT HYPOTHETICAL. On 2026-07-27 a collapsed BM25 arm
// behind a healthy HNSW arm produced 693 publish skips in 55 minutes and was cured
// only by a manual rebuild; the diagnosis was that the degeneracy probe asked the
// vector arm alone.

// bm25HealPending and bm25HealArmed carry the gate's process-local no-progress
// bound, keyed by graph. The two-map split is what makes the bound consumable only
// by a rebuild that actually COMPLETED:
//
//   - PENDING is written when the gate returns true. It records the arm's resident
//     count at the moment the rebuild was authorized, and does not yet consume the
//     one shot the bound allows.
//   - ARMED is promoted from pending only after a rebuild returned ran=true with a
//     nil error. Only this map is consulted by the decline check.
//
// A rebuild that never ran — a single-flight coalesce (ran=false, nil error) or a
// rebuild error — therefore cannot consume the shot. A rebuild that did not run
// cannot have failed to raise the arm, and arming on one would let a single
// transient error disable BM25 self-heal for the lifetime of the process.
//
// UNLIKE the rebuild single-flight this idiom mirrors, these maps do NOT self-clear
// once the work finishes: an armed record persists until the arm recovers. TESTS
// MUST THEREFORE CALL resetBM25HealProgress (via t.Cleanup) — package state left
// armed by one test silently makes the next test's rebuild decline.
//
// CONCURRENCY: the gate is reached both from the reconcile-loop goroutine and from
// the per-collector heal closures, so every read-modify-write of either map takes
// bm25HealMu. The lock is held only around the map access, never across a probe.
var (
	bm25HealMu      sync.Mutex
	bm25HealPending = map[string]int{}
	bm25HealArmed   = map[string]int{}
)

// bm25HealKey keys the no-progress maps by graph, matching the composite key the
// rebuild single-flight builds for the same (graph type, name) pair.
func bm25HealKey(gt kgtypes.GraphType, name string) string { return string(gt) + "/" + name }

// resetBM25HealProgress clears BOTH no-progress maps. TEST-ONLY: the maps are
// package-level and do not self-clear, so every test that can reach the gate must
// register this via t.Cleanup or it contaminates the tests that follow it.
func resetBM25HealProgress() {
	bm25HealMu.Lock()
	defer bm25HealMu.Unlock()
	clear(bm25HealPending)
	clear(bm25HealArmed)
}

// clearBM25HealProgress drops both records for one graph — the arm recovered, so a
// future collapse gets a fresh shot rather than inheriting a stale bound.
func clearBM25HealProgress(gt kgtypes.GraphType, name string) {
	key := bm25HealKey(gt, name)
	bm25HealMu.Lock()
	defer bm25HealMu.Unlock()
	delete(bm25HealPending, key)
	delete(bm25HealArmed, key)
}

// armBM25HealProgress promotes a graph's PENDING record to ARMED, consuming the one
// shot the no-progress bound allows. It is called at every site where a rebuild's
// (ran, err) outcome is visible, under `ran && err == nil`.
//
// IT IS A NO-OP WHEN NO PENDING RECORD EXISTS, and that guard still earns its place
// under the single surviving heal path. healNeedsRebuild SHORT-CIRCUITS on the HNSW
// arm: a graph whose vector corpus is empty or degenerate returns true from
// healNeedsRebuildLocal and reaches a rebuild WITHOUT this gate ever being consulted.
// Arming on that outcome would record a bound for an arm the gate never examined,
// poisoning the next BM25 decision with a resident value it never chose. The guard is
// on the pending record being present, not on which caller is arming — which is what
// keeps it correct as the set of rebuild-reaching paths changes.
func (c *client) armBM25HealProgress(gt kgtypes.GraphType, name string) {
	key := bm25HealKey(gt, name)
	bm25HealMu.Lock()
	defer bm25HealMu.Unlock()
	resident, pending := bm25HealPending[key]
	if !pending {
		return // no gate-authorized rebuild outstanding for this graph.
	}
	delete(bm25HealPending, key)
	bm25HealArmed[key] = resident
}

// armObservationFor selects one format's observation out of a per-format probe
// result.
func armObservationFor(obs []segmentdist.ArmObservation, format string) (segmentdist.ArmObservation, bool) {
	for _, v := range obs {
		if v.Format == format {
			return v, true
		}
	}
	return segmentdist.ArmObservation{}, false
}

// healNeedsRebuildBM25 is the BM25 arm of the rebuild decision, reached once the
// HNSW arm has decided it does not need a rebuild of its own. It runs three ordered
// checks, and every one of them declines by default.
//
//  1. PRESENCE. The graph must HOLD a BM25 corpus in its L2 cache. A graph with
//     embedded nodes but no BM25 corpus at all is deliberately OUT of scope: that
//     population recovers through ordinary indexing traffic, and rebuilding it here
//     would fire a rebuild for every such graph on the first tick.
//     THE OPERAND IS THE CACHE, NOT A RESIDENT COUNT. It was a shipped BM25 manifest
//     until the cloud segment rail was deleted; the local restatement is
//     CachedSegmentCount, which reads what is on disk. A resident count would read 0
//     for an EVICTED pool and report a graph with a complete corpus as never having
//     produced one.
//  2. RATIO. The BM25 arm must be degenerate against the graph's EMBEDDED-node count,
//     via the same degenerateAgainstEmbedded predicate healNeedsRebuildLocal uses —
//     one predicate, so the HNSW and BM25 arms cannot drift into disagreeing about
//     what degenerate means. An arm that could not be measured never drives a
//     rebuild, and an arm that is healthy clears the no-progress bound on its way out.
//  3. NO-PROGRESS BOUND. A rebuild that already completed for this graph without
//     raising the arm's resident count is not repeated. Without this the arm would
//     be rebuilt on every tick forever whenever a rebuild cannot converge — and the
//     existing heal breaker cannot catch that case, because it measures progress
//     against the HNSW corpus, which on this shape is already complete and so
//     records progress (resetting the streak) after every pass.
//
// obs is the already-computed per-format observation set when the caller has one;
// pass nil to have the gate probe for itself.
func (c *client) healNeedsRebuildBM25(
	ctx context.Context, gt kgtypes.GraphType, name string, obs []segmentdist.ArmObservation,
) (bool, error) {
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	return c.healNeedsRebuildBM25With(ctx, gt, name, obs, embedded)
}

// healNeedsRebuildBM25With is healNeedsRebuildBM25 for a caller that already read the
// embedded-node denominator. The denominator is per-GRAPH, so a caller evaluating
// both arms reads it once and hands the same number to each.
func (c *client) healNeedsRebuildBM25With(
	ctx context.Context, gt kgtypes.GraphType, name string,
	obs []segmentdist.ArmObservation, embedded int,
) (bool, error) {
	format := bm25.New().Name()

	// 1. PRESENCE.
	if c.segmentMgr.CachedSegmentCount(gt, name, format) == 0 {
		return false, nil // no BM25 corpus on disk — not this gate's to heal.
	}

	// 2. RATIO.
	var err error
	if obs == nil {
		if obs, err = c.segmentMgr.ResidentObservationsByFormat(ctx, gt, name); err != nil {
			return false, err
		}
	}
	v, ok := armObservationFor(obs, format)
	if !ok || v.Err != nil {
		// Unmeasured arm: best-effort — an arm that could not be read must never
		// drive a rebuild.
		return false, nil //nolint:nilerr // an unmeasured arm declines rather than propagating
	}
	// AN EVICTED ARM DECLINES WITHOUT CLEARING, and it must be tested BEFORE the
	// degeneracy test below. An evicted arm reports ResidentAfterLoad 0, which
	// degenerateAgainstEmbedded reads as a lost pool and turns into a rebuild —
	// undoing the eviction at the highest possible cost. Declining here is also what
	// keeps the no-progress bound (check 3, the only thing stopping an endless
	// per-tick rebuild) from being cleared on the strength of a measurement nobody
	// took.
	if v.Evicted {
		return false, nil
	}
	// THE DENOMINATOR IS THE GRAPH'S EMBEDDED-NODE COUNT, supplied by the caller and
	// applied to THIS format's resident numerator. It is per-GRAPH while the numerator
	// is per-FORMAT, which is exactly why the arms stay separately answerable off one
	// read.
	if !degenerateAgainstEmbedded(v.ResidentAfterLoad, embedded) {
		clearBM25HealProgress(gt, name)
		return false, nil
	}

	// 3. NO-PROGRESS BOUND.
	key := bm25HealKey(gt, name)
	bm25HealMu.Lock()
	armed, isArmed := bm25HealArmed[key]
	declined := isArmed && v.ResidentAfterLoad <= armed
	if !declined {
		bm25HealPending[key] = v.ResidentAfterLoad
	}
	bm25HealMu.Unlock()
	if declined {
		slog.Warn("bootstrap: BM25 arm rebuild declined — prior rebuild did not raise the arm's resident (no-progress bound)",
			"graph_type", gt, "name", name, "resident", v.ResidentAfterLoad, "armed_at", armed)
		return false, nil
	}
	return true, nil
}
