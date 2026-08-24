// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// The BM25 arm of the auto-heal rebuild decision.
//
// healNeedsRebuild's shipped-completeness check is scoped to the HNSW format: it
// asks whether the HNSW corpus covers the embedded node count, and returns
// no-rebuild when it does. A graph can have a healthy HNSW arm and a COLLAPSED BM25
// arm at the same time — the two formats ship separate manifests and load from
// separately-rooted L2 caches — and on that shape the HNSW-scoped check short-
// circuits before the BM25 arm is ever consulted, so the collapsed arm has no
// trigger to recover through. This file is the second arm of that decision.
//
// The BM25 arm is safe to escalate to a rebuild in a way the HNSW arm is not.
// RebuildSegments populates the DETERMINISTIC HNSW engine, a different instance
// from the HNSW engine the read path (and therefore the degeneracy probe) reads,
// so an HNSW rebuild cannot raise the count it is measured against — hence the
// HNSW arm's no-rebuild-when-shipped-complete rule. BM25 has no deterministic
// variant: a rebuild writes the SAME BM25 engine the embed and read paths use, so
// it genuinely raises the read pool and converges.

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
// IT IS A NO-OP WHEN NO PENDING RECORD EXISTS. Two of healNeedsRebuild's paths
// reach a rebuild WITHOUT consulting this gate (a never-shipped HNSW corpus, and a
// degenerate HNSW arm a read-engine load could not restore). Arming on one of those
// would record a bound for an arm the gate never examined, poisoning the next BM25
// decision with a resident value it never chose. The guard is on the pending record
// being present, not on which caller is arming.
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

// armVerdictFor selects one format's verdict out of a per-format probe result.
func armVerdictFor(verdicts []segmentdist.ArmVerdict, format string) (segmentdist.ArmVerdict, bool) {
	for _, v := range verdicts {
		if v.Format == format {
			return v, true
		}
	}
	return segmentdist.ArmVerdict{}, false
}

// hnswArmProbe runs the per-format degeneracy probe ONCE and reports the HNSW arm's
// verdict, returning the whole verdict slice so the caller can hand it to the BM25
// arm instead of paying a second probe.
//
// The returned error folds the top-level probe error together with the HNSW arm's
// OWN ArmVerdict.Err, which preserves the caller's existing semantics exactly: an
// HNSW arm that could not be read takes the probe-error path (warn, keep the
// existing resident, do not rebuild) rather than being mistaken for an arm whose
// load restored coverage. AN EVICTED ARM TAKES THAT SAME PATH, because an evicted
// arm was not read: the residency budget unloaded its pool and the probe declined
// to resurrect it, so its Degenerate false is the absence of a measurement rather
// than a healthy one.
func (c *client) hnswArmProbe(
	ctx context.Context, gt kgtypes.GraphType, name string,
) ([]segmentdist.ArmVerdict, bool, error) {
	verdicts, err := c.segmentMgr.ReconcileResidentDegenerateByFormat(ctx, gt, name)
	if err != nil {
		return verdicts, false, err
	}
	v, ok := armVerdictFor(verdicts, hnsw.New().Name())
	if !ok {
		return verdicts, false, errors.New("segment probe returned no hnsw arm verdict")
	}
	if v.Err != nil {
		return verdicts, false, v.Err
	}
	if v.Evicted {
		return verdicts, false, errors.New("hnsw arm is evicted — its segment pool was unloaded and not measured")
	}
	return verdicts, v.Degenerate, nil
}

// healNeedsRebuildBM25 is the BM25 arm of the rebuild decision, reached once the
// HNSW arm has decided it does not need a rebuild of its own. It runs three ordered
// checks, and every one of them declines by default.
//
//  1. PRESENCE. The graph must have a shipped BM25 manifest. A graph with embedded
//     nodes but no shipped BM25 corpus at all is deliberately OUT of scope: that
//     population recovers through ordinary indexing traffic, and rebuilding it here
//     would fire a rebuild for every such graph on the first tick.
//  2. RATIO. The BM25 arm must be degenerate against its OWN manifest's doc count.
//     An arm that could not be measured never drives a rebuild, and an arm that is
//     healthy clears the no-progress bound on its way out.
//  3. NO-PROGRESS BOUND. A rebuild that already completed for this graph without
//     raising the arm's resident count is not repeated. Without this the arm would
//     be rebuilt on every tick forever whenever a rebuild cannot converge — and the
//     existing heal breaker cannot catch that case, because it measures progress
//     against the HNSW corpus, which on this shape is already complete and so
//     records progress (resetting the streak) after every pass.
//
// verdicts is the already-computed per-format probe result when the caller has one;
// pass nil to have the gate probe for itself.
func (c *client) healNeedsRebuildBM25(
	ctx context.Context, gt kgtypes.GraphType, name string, verdicts []segmentdist.ArmVerdict,
) (bool, error) {
	format := bm25.New().Name()

	// 1. PRESENCE.
	snapshot, err := c.segmentMgr.ShippedManifestSnapshot(ctx, gt, name, format)
	if err != nil {
		return false, err
	}
	if !c.segmentMgr.HasShippedFromSnapshot(snapshot) {
		return false, nil // nothing shipped for this format — not this gate's to heal.
	}

	// 2. RATIO.
	if verdicts == nil {
		if verdicts, err = c.segmentMgr.ReconcileResidentDegenerateByFormat(ctx, gt, name); err != nil {
			return false, err
		}
	}
	v, ok := armVerdictFor(verdicts, format)
	if !ok || v.Err != nil {
		// Unmeasured arm: best-effort, matching the probe-error convention above —
		// an arm that could not be read must never drive a rebuild.
		return false, nil //nolint:nilerr // an unmeasured arm declines rather than propagating
	}
	// AN EVICTED ARM DECLINES WITHOUT CLEARING, and it must be tested BEFORE the
	// Degenerate branch below. An evicted arm reports Degenerate false, so falling
	// into that branch would call clearBM25HealProgress and drop the no-progress
	// bound — the only thing stopping check 3's endless per-tick rebuild — on the
	// strength of a measurement nobody took. This is the same decline the unmeasured
	// arm above performs, for the same reason.
	if v.Evicted {
		return false, nil
	}
	if !v.Degenerate {
		clearBM25HealProgress(gt, name)
		return false, nil
	}

	// 3. NO-PROGRESS BOUND.
	key := bm25HealKey(gt, name)
	bm25HealMu.Lock()
	armed, isArmed := bm25HealArmed[key]
	declined := isArmed && v.ResidentAfterRecover <= armed
	if !declined {
		bm25HealPending[key] = v.ResidentAfterRecover
	}
	bm25HealMu.Unlock()
	if declined {
		slog.Warn("bootstrap: BM25 arm rebuild declined — prior rebuild did not raise the arm's resident (no-progress bound)",
			"graph_type", gt, "name", name, "resident", v.ResidentAfterRecover, "armed_at", armed)
		return false, nil
	}
	return true, nil
}
