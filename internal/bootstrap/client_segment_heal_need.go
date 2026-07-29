// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// healNeedsRebuild is the embed-drain auto-heal's decision: whether (gt, name)
// needs a from-scratch RebuildSegments, or is healthy / restorable by a cheaper
// read-engine load. Steps 1-3 decide the HNSW arm; the two that clear it hand off
// to the BM25 arm (healNeedsRebuildBM25) — the formats ship SEPARATE manifests.
//
//  1. zero shipped segments (never-shipped) → rebuild (true).
//  2. degenerate-but-nonzero pool → try a one-shot read-engine load FIRST
//     (hnswArmProbe cache-first load()s the intact corpus and re-probes coverage).
//     If the load restored coverage (resDegen=false) the pool was merely not-yet-
//     resident (a daemon restart) → the BM25 arm decides. Only when the load cannot
//     restore coverage (resDegen=true) → rebuild (true). A probe error
//     (server down / timing out) is best-effort: WARN and do NOT rebuild —
//     rebuilding against a down/timing-out server is futile, so keep the existing
//     resident and wait for a successful probe rather than storm a from-scratch
//     rebuild on a transient failure.
//  3. healthy HNSW (segments present AND covering enough) → the BM25 arm decides.
//
// OSS L2-AUTHORITATIVE BRANCH (decouple #3): when the segment manager is
// L2-authoritative (not logged in — no server segment store), the decision runs
// entirely locally via healNeedsRebuildLocal BEFORE the ShippedManifestSnapshot
// below, so the OSS heal path touches only the local L2. On the not-logged-in path
// ShippedManifestSnapshot resolves to the L2-local source, so returning above it
// keeps the OSS heal path local-only. The cloud path below reads the GCS manifest.
func (c *client) healNeedsRebuild(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	if c.segmentMgr.IsL2Authoritative(gt, name) {
		return c.healNeedsRebuildLocal(ctx, gt, name)
	}
	// ONE manifest snapshot per heal pass: the presence probe (HasShippedFromSnapshot)
	// and the coverage probe (segmentPoolDegenerate → ShippedDocCountFromSnapshot) both
	// derive from this single List(0) instead of each issuing their own.
	snapshot, err := c.segmentMgr.ShippedManifestSnapshot(ctx, gt, name, hnsw.New().Name())
	if err != nil {
		return false, err
	}
	if !c.segmentMgr.HasShippedFromSnapshot(snapshot) {
		return true, nil // zero shipped segments — rebuild from the embedded nodes.
	}
	degenerate, err := c.segmentPoolDegenerate(ctx, gt, name, snapshot)
	if err != nil {
		return false, err
	}
	if !degenerate {
		// The HNSW arm is healthy or disarmed — the BM25 arm decides (it probes).
		return c.healNeedsRebuildBM25(ctx, gt, name, nil)
	}
	// Degenerate-but-nonzero pool: the shipped corpus is present but the live
	// searchable pool is below coverage. Before paying a from-scratch rebuild, try a
	// one-shot read-engine load of the intact persisted corpus.
	verdicts, resDegen, rerr := c.hnswArmProbe(ctx, gt, name)
	if rerr != nil {
		slog.Warn("bootstrap: auto-heal load-first probe failed; keeping existing resident (no rebuild)",
			"graph_type", gt, "name", name, "err", rerr)
		// Best-effort: a probe error (server down / timing out) does NOT rebuild —
		// rebuilding against a down/timing-out server is futile, so keep the existing
		// resident and wait for a successful probe rather than storm a from-scratch
		// rebuild on a transient failure.
		return false, nil
	}
	if !resDegen {
		// Load restored the HNSW arm's coverage — the BM25 arm decides (verdicts reused).
		slog.Info("bootstrap: auto-heal restored degenerate pool via read-engine load (no rebuild)",
			"graph_type", gt, "name", name)
		return c.healNeedsRebuildBM25(ctx, gt, name, verdicts)
	}
	// The load could NOT restore coverage — the genuinely missing/collapsed case.
	return true, nil
}

// healNeedsRebuildLocal is the OSS (L2-authoritative) heal decision: it computes
// degeneracy from LOCAL operands ONLY — the L2 resident doc count (numerator) and the
// embedded-node count (denominator) — with NO server presence probe, NO server
// re-import, and NO ReconcileResidentDegenerate leg. This is the decouple #3 collapse:
// on the OSS path there is no server segment store, so the local presence signal is
// resident==0 (an empty/lost L2), not a server List. The whole call issues ZERO
// SegmentService RPC (LoadResidentDocCount is L2-only per Phase 3; GraphEmbeddedCount
// reads the node graph via the Engine Stats RPC, a separate local-store axis).
//
// Decision order (a floor-LESS resident==0 one-shot AHEAD of a floor-gated ratio —
// the OSS heal derives presence locally, with no server probe):
//
//  1. ONE-SHOT EMPTY-POOL TRIGGER, BEFORE the floor gate: an empty/lost L2
//     (resident==0) with embedded nodes present rebuilds REGARDLESS of magnitude. It
//     is the LOCAL analog of the original floor-LESS zero-segments presence probe
//     (the "Below the floor only the zero-segments probe heals" rule the floor never
//     disarmed — see the segmentCoverageFloor doc above). It restores the sub-floor
//     zero-presence heal a pure floor-gated ratio would drop: an OSS graph with e.g.
//     30 embedded and a lost L2 → resident=0, embedded=30<64 → 30 nodes permanently
//     unsearchable until growth past the floor or a manual rebuild_segments.
//
//     NON-FLAPPING IN PRACTICE — but NOT because the rebuild makes resident>0:
//     tools.RebuildSegments populates the DETERMINISTIC engine (Manager.detManagers,
//     via AddDeterministic/FlushDeterministic) and ships to L2, NOT the embed manager
//     that LoadResidentDocCount reads (Manager.managers), and that read is the
//     l2Loaded-once-guarded load(), so resident stays 0 immediately after a rebuild.
//     The trigger self-clears when a LATER event raises the embed engine's resident
//     set: a subsequent embed-drain AddAndShip (normal collect), a process restart
//     (fresh load()→loadResidentFromL2 imports the shipped L2 set), or the search-side
//     recover. A search-less re-probe before any of those correctly RE-FIRES the
//     trigger — a bounded, idempotent no-op churn (RebuildSegments is a content-hash
//     no-op), which is acceptable.
//
//  2. TINY-GRAPH RATIO NO-FLAP: below the floor the resident/embedded ratio is too
//     noisy to consult, but a non-empty L2 (resident>0) has already cleared the
//     one-shot above, so a small HEALTHY graph never churns.
//
//  3. RATIO PROBE: resident covering below coverageRatioThreshold of the embedded
//     corpus marks the pool degenerate.
//
// The OSS manage(status) coverage column is now ALSO L2-sourced: with the
// SegmentService deleted, Manager.ShippedSegmentDocCount branches on
// IsL2Authoritative and reads the L2 resident HNSW doc count (LoadResidentDocCount)
// on the OSS path instead of a server List(0), so the whole OSS segment surface
// (heal AND status) is server-free.
func (c *client) healNeedsRebuildLocal(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	// Both operands are LOCAL (zero SegmentService RPC). GraphEmbeddedCount is the
	// node-graph denominator (same one segmentPoolDegenerate uses); on error return
	// (false, err), matching segmentPoolDegenerate's convention.
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	// The L2 resident numerator (load-first, L2-only on the OSS path). A load failure
	// is best-effort: WARN and do NOT rebuild — never storm a rebuild on a transient
	// failure (mirroring the probe-error convention in healNeedsRebuild).
	resident, err := c.segmentMgr.LoadResidentDocCount(ctx, gt, name)
	if err != nil {
		slog.Warn("bootstrap: OSS auto-heal L2 resident-count load failed; keeping existing resident (no rebuild)",
			"graph_type", gt, "name", name, "err", err)
		return false, nil
	}

	// 1. One-shot empty-pool trigger, BEFORE the floor gate (see method doc).
	if resident == 0 && embedded > 0 {
		return true, nil
	}
	// 2. Tiny-graph ratio no-flap: a non-empty L2 (resident>0) already cleared the
	//    one-shot, so a sub-floor healthy graph never churns.
	if embedded < segmentCoverageFloor {
		return false, nil
	}
	// 3. Ratio probe.
	return float64(resident) < coverageRatioThreshold*float64(embedded), nil
}

// segmentPoolDegenerate reports whether a graph's shipped segment pool is present
// but DEGENERATE — covering far fewer docs than the graph has embedded — and so
// should be rebuilt. It is consulted only when the caller already found segments
// from the SAME snapshot (the zero case heals unconditionally upstream).
//
// It derives the HNSW doc count from the passed-in snapshot (the shared List(0)
// healNeedsRebuild already fetched — no second List), and disarms (returns false)
// conservatively in three cases so a healthy or ambiguous graph never churns: (1)
// anyUnknown — at least one shipped HNSW segment has doc_count==0, so coverage is
// unknowable (migration-storm guard); (2) embedded below segmentCoverageFloor — too
// small for the ratio to be meaningful; (3) covered at/above coverageRatioThreshold
// × embedded — the pool is healthy.
func (c *client) segmentPoolDegenerate(
	ctx context.Context, gt kgtypes.GraphType, name string, snapshot []searchengine.SegmentMeta,
) (bool, error) {
	covered, anyUnknown := c.segmentMgr.ShippedDocCountFromSnapshot(snapshot, hnsw.New().Name())
	if anyUnknown {
		// Conservative-unknown: an old pre-doc_count segment is present, so the
		// ratio is not trustworthy — disarm and leave it to the zero-only trigger.
		return false, nil
	}
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	if embedded < segmentCoverageFloor {
		// Small-graph no-flap: too few embedded nodes for the ratio to be meaningful.
		return false, nil
	}
	return float64(covered) < coverageRatioThreshold*float64(embedded), nil
}
