// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentCacheDirFor is the L2 disk-cache root for client-built/pulled HNSW
// segment blobs: filepath.Join(root, "segments"), where root is the already
// tilde-expanded --graph-storage data root the daemon was started with. This is
// the SAME expression the server resolves its own segment store under
// (filepath.Join(<graph-storage>, "segments")), so client L2 and server store
// co-locate over a shared root — the successor to the retired HOME-fixed
// segmentCacheDir() that unconditionally returned <home>/.knowledge/segments.
func segmentCacheDirFor(root string) string {
	return filepath.Join(root, "segments")
}

// ensureSegmentManager constructs the client-side segment Manager
// UNCONDITIONALLY (router-guarded, idempotent), so the READ/CONSUME engine the
// search intercepts query via SegmentManager() exists even on an offline daemon
// (--no-llm-pipeline, or no embedder/summarizer configured). The read path then
// serves BM25 over the already-shipped segments rather than erroring "client
// segment engine unavailable". segmentdist.NewManager needs no embedder — the
// embedder/LLM is required ONLY for index UPDATES (fresh embeddings, segment
// rebuild/extension), which stay inside the embedder-gated body of
// wirePipelineRuntime, so this construction never triggers an auto-UPDATE offline.
//
// The L2 cache roots at segmentCacheDirFor(graphStorage) — i.e.
// <graph-storage>/segments off the CLIENT's --graph-storage data root, not a
// HOME-fixed path. Since the client spawns its auto-local server with the same
// --graph-storage value (maybeSpawnLocalServer), the server resolves its segment
// store under the identical <graph-storage>/segments, so client L2 and server
// store co-locate. Under a manually-split client/server --graph-storage the cache
// follows the CLIENT's root; either way it is strictly better than the old
// unconditional HOME-fix. For the default root (~/.knowledge/, tilde-expanded by
// the client to <home>/.knowledge) the location is unchanged: <home>/.knowledge/segments.
//
// This is the PRODUCTION construction site: wireRuntimesBackground (daemon.go)
// calls it BEFORE wirePipelineRuntime, where c.router is already set
// synchronously by constructClient — so the router guard is sufficient and there
// is no race. wirePipelineRuntime then only ATTACHES this already-built instance
// to the producer (AttachSegmentManager) for shipping. The router guard leaves a
// router-less headless client at nil (the only remaining nil-Manager state); the
// `== nil` guard makes a repeat call a no-op.
func (c *client) ensureSegmentManager(graphStorage string) {
	if c.router != nil && c.segmentMgr == nil {
		c.segmentMgr = segmentdist.NewManager(c.router, segmentCacheDirFor(graphStorage), 0)
	}
}

// SegmentManager returns the SAME *segmentdist.Manager the client holds — the one
// per-graph BM25+HNSW segment owner the producer ships into and the search
// intercepts consume. It is constructed UNCONDITIONALLY in wireRuntimesBackground
// (ensureSegmentManager, router-gated, before wirePipelineRuntime), so the read
// path serves offline; wirePipelineRuntime only ATTACHES that same instance to the
// producer when the pipeline wires.
// Returns an UNTYPED nil interface (not a typed nil *Manager wrapped in the
// interface) in two states: a router-less / headless client (construction skipped),
// or the bind-first wiring window (bind-first startup) before ensureSegmentManager
// has run. The search arms do NOT fall back to a server search (that path is
// retired); instead they gate on PipelineReady() and return a "daemon still
// starting" not-ready error during the window, so a nil here is never dereferenced.
func (c *client) SegmentManager() tools.SegmentSearcher {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentVectorResolver returns the SAME *segmentdist.Manager as the by-id
// stored-vector read seam the mode:"similar" search claim resolves its query
// vector through (Manager.VectorByID). Returns an UNTYPED nil interface (not a
// typed nil *Manager) when the pipeline was not wired, so the similar-mode claim's
// nil-guard fires correctly and loud-errors instead of a silent empty result.
func (c *client) SegmentVectorResolver() tools.SegmentVectorResolver {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentCoverage returns the SAME *segmentdist.Manager as the read seam the
// manage(status) segment-coverage column reads segment-covered doc counts through
// (Manager.ShippedSegmentDocCount). Returns an UNTYPED nil interface (not a typed
// nil *Manager) when the pipeline was not wired, so the column's nil-guard fires
// and renders a placeholder instead of dereferencing.
func (c *client) SegmentCoverage() tools.SegmentCoverageReader {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentShipper returns the SAME *segmentdist.Manager as the build-concurrent/
// ship-once SHIP surface the rebuild_segments driver drives (AddDeterministic /
// AddFields / FlushDeterministic / InvalidateLocal). Returns an UNTYPED nil
// interface (not a typed nil *Manager) when the pipeline was not wired, so the
// driver's nil-guard fires correctly.
func (c *client) SegmentShipper() tools.SegmentShipper {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentPruner returns the one-shot manage(prune-cache) orphaned-L2-reclaim seam
// wrapping the SAME *segmentdist.Manager the client holds. Returns an UNTYPED nil
// interface (not a typed-nil adapter) when the segment manager was not constructed
// (router-less / headless client), so the handler's nil-guard fires and surfaces a
// not-ready error rather than dereferencing.
//
// The returned segmentPrunerAdapter is the ONLY place the tools-local and
// segmentdist-native prune vocabularies meet: it maps the tools seam's PARALLEL
// (graphTypes, names) slices into []segmentdist.PruneCacheTarget, calls
// Manager.PruneCache, and maps the segmentdist-native report back into the
// tools-local report — keeping the tools package free of any segmentdist import.
func (c *client) SegmentPruner() tools.SegmentPruner {
	if c.segmentMgr == nil {
		return nil
	}
	return segmentPrunerAdapter{mgr: c.segmentMgr}
}

// segmentPrunerAdapter bridges the tools.SegmentPruner seam (parallel slices +
// tools-local report) to segmentdist.Manager.PruneCache (native target/report
// types). It lives here because client_segment.go is the one bootstrap file that
// legitimately imports BOTH tools and segmentdist.
type segmentPrunerAdapter struct {
	mgr *segmentdist.Manager
}

// PruneCache maps the tools seam's parallel (graphTypes, names) slices into the
// segmentdist-native target shape, calls the Manager, and copies the native report
// back field-for-field into the tools-local report. The two slices are paired by
// index (graphTypes[i] with names[i]); a length mismatch is defensively truncated
// to the shorter slice so a malformed call can never index out of range.
func (a segmentPrunerAdapter) PruneCache(
	ctx context.Context, graphTypes []kgtypes.GraphType, names []string, execute bool,
) (tools.PruneCacheReport, error) {
	n := min(len(names), len(graphTypes))
	targets := make([]segmentdist.PruneCacheTarget, 0, n)
	for i := range n {
		targets = append(targets, segmentdist.PruneCacheTarget{GraphType: graphTypes[i], Name: names[i]})
	}

	rep, err := a.mgr.PruneCache(ctx, targets, execute)
	if err != nil {
		return tools.PruneCacheReport{}, err
	}

	out := tools.PruneCacheReport{
		Removed:      rep.Removed,
		RemovedBytes: rep.RemovedBytes,
		Graphs:       make([]tools.PruneCacheGraphReport, 0, len(rep.Graphs)),
	}
	for _, g := range rep.Graphs {
		out.Graphs = append(out.Graphs, tools.PruneCacheGraphReport{
			GraphType:   g.GraphType,
			Name:        g.Name,
			Format:      g.Format,
			Orphans:     g.Orphans,
			Bytes:       g.Bytes,
			Aborted:     g.Aborted,
			AbortReason: g.AbortReason,
		})
	}
	return out, nil
}

// PipelineScanner returns the login-routed PipelineScan+Execute wire seam the
// rebuild_segments driver pages the segment_rebuild scan through. It reuses
// routedWireClient (the same per-call cloud-when-logged-in / local-otherwise
// adapter the client pipeline scans through). Returns an UNTYPED nil interface
// when no router is wired (degraded headless mode) so the driver's nil-guard
// fires correctly.
func (c *client) PipelineScanner() tools.PipelineScanner {
	if c.router == nil {
		return nil
	}
	return routedWireClient{router: c.router}
}

// Coverage-heal gate constants. The auto-heal arm no longer triggers only on a
// ZERO-segment pool — it also heals a DEGENERATE-but-nonzero pool (segments
// present, but covering far fewer docs than the graph has embedded). Two
// thresholds keep it from flapping:
//
//   - segmentCoverageFloor: the absolute embedded-count MAGNITUDE below which the
//     ratio probe is NEVER consulted — a small graph (e.g. a handful of embedded
//     nodes) can legitimately sit in one small segment, so the ratio is noisy
//     there. Below the floor only the zero-segments probe heals (never the ratio),
//     so a tiny healthy graph never churns.
//   - coverageRatioThreshold: covered/embedded BELOW this fraction marks the pool
//     degenerate (the live incident was ~6-of-60 shards covering a fraction of the
//     embedded corpus). At/above it the pool is healthy and the arm disarms.
const (
	segmentCoverageFloor   = 64
	coverageRatioThreshold = 0.5
)

// buildHealFactory constructs the auto-heal closure factory the pipeline
// injects into each collector (Pipeline.AttachHealFactory). It is the ONLY layer
// where the pipeline, the segmentdist probe, and the tools rebuild driver are all
// visible — keeping the rebuild body OUT of the pipeline package avoids a
// pipeline→tools import cycle (tools already imports pipeline).
//
// The returned factory yields, per (gt, name), a per-collector heal closure (or
// nil for any graph with no rebuildable segments). Auto-heal is scoped to the
// embeddable builtins kgtypes.HasRebuildableSegments admits — knowledge, code,
// cloud, cicd, practice — the SAME gate the manual rebuild_segments op uses
// (handleClientRebuildSegments), so the auto-heal arm and the manual rebuild gate
// cannot drift; the non-embeddable builtins (linkage, transformers) and the raw
// graphs (logs, web, pdf) have no segments to heal and get a nil closure. The
// closure runs on the armed embed drain edge: a CHEAP presence + coverage probe
// and, when a from-scratch rebuild is genuinely needed, the rebuild driver
// (single-flight, shared with the manual rebuild_segments op). A healthy graph
// (segments present AND covering enough of the embedded corpus) is a probe +
// disarm, never a churn.
//
// The probe makes a THREE-way decision (the middle path is the fix):
//  1. zero shipped segments (the never-shipped case) → rebuild from scratch.
//  2. a degenerate-but-nonzero pool (segment-covered docs — summed HNSW doc_count —
//     below coverageRatioThreshold × the graph's embedded-node count, once the
//     embedded count clears segmentCoverageFloor) → try a one-shot read-engine
//     load FIRST via Manager.ReconcileResidentDegenerate, which cache-first
//     load()s the intact persisted corpus and re-probes coverage. If that load
//     restores resident coverage (degenerate=false) the pool was intact-but-not-
//     resident — a daemon restart whose searchable pool simply had not loaded yet
//     — so SKIP the rebuild; the load alone healed it. Rebuild from scratch ONLY
//     when the load cannot restore coverage (degenerate=true) — the genuinely
//     missing/collapsed shipped case. (A ReconcileResidentDegenerate error is
//     best-effort: WARN and do NOT rebuild — rebuilding against a down/timing-out
//     server is futile, so wait for a successful probe and keep the existing
//     resident instead.)
//  3. healthy (segments present AND covering enough) → probe + disarm, never a churn.
//
// CONSERVATIVE-UNKNOWN guard: a segment whose doc_count is 0 predates the
// doc_count wire plumbing, so its real coverage is UNKNOWN. When ANY shipped HNSW
// segment reports doc_count==0 (ShippedSegmentDocCount's anyUnknown), the ratio
// probe is DISARMED and the arm falls back to the zero-only trigger — without this
// a fleet mid-migration (every shipped meta still 0) would read covered=0 on every
// healthy graph and trigger a fleet-wide rebuild storm. The guard self-retires per
// graph: the first heal/rebuild re-ships segments carrying real doc_count.
func (c *client) buildHealFactory() func(kgtypes.GraphType, string) func(context.Context) error {
	return func(gt kgtypes.GraphType, name string) func(context.Context) error {
		// Rebuildable-segments gate FIRST — the auto-heal closure is built only for
		// graphs that carry rebuildable segments (the embeddable builtins: knowledge,
		// code, cloud, cicd, practice). This is the SAME kgtypes.HasRebuildableSegments
		// predicate handleClientRebuildSegments gates the manual rebuild_segments op
		// on, so the auto-heal arm and the manual rebuild gate cannot drift. The
		// non-embeddable builtins (linkage, transformers) and raw graphs (logs, web,
		// pdf) have no segments to heal and return a nil closure.
		if !kgtypes.HasRebuildableSegments(gt) {
			return nil
		}
		return func(ctx context.Context) error {
			needsRebuild, err := c.healNeedsRebuild(ctx, gt, name)
			if err != nil {
				return err
			}
			if !needsRebuild {
				// Healthy pool, or a degenerate pool a read-engine load already
				// restored to coverage — no from-scratch rebuild.
				return nil
			}
			// Zero shipped segments OR a degenerate pool a read-engine load could not
			// restore: heal by rebuilding from the already-embedded nodes. Reuse the
			// SAME login-routed scanner seam the manual op uses (the accessor carries
			// the c.router==nil guard) and the SAME segment manager as shipper.
			// RebuildSegments owns the single-flight shared with the manual op.
			scanner := c.PipelineScanner()
			ran, scanned, built, partial, pruned, err := tools.RebuildSegments(ctx, scanner, c.segmentMgr, gt, name)
			if err != nil {
				return err
			}
			slog.Info("bootstrap: auto-heal rebuilt segments after read-engine load could not restore coverage for builtin graph",
				"graph_type", gt, "name", name, "ran", ran, "scanned", scanned, "built", built, "partial", partial, "pruned", len(pruned))
			return nil
		}
	}
}

// healNeedsRebuild is the embed-drain auto-heal's THREE-way decision: it
// reports whether (gt, name) needs a from-scratch RebuildSegments, or whether the
// pool is already healthy / restorable by a cheaper read-engine load.
//
//  1. zero shipped segments (never-shipped) → rebuild (true).
//  2. degenerate-but-nonzero pool → try a one-shot read-engine load FIRST
//     (Manager.ReconcileResidentDegenerate cache-first load()s the intact persisted
//     corpus and re-probes coverage). If the load restored coverage (resDegen=false)
//     the pool was intact-but-not-resident (a daemon restart whose searchable pool
//     simply had not loaded yet) → SKIP the rebuild (false). Only when the load
//     cannot restore coverage (resDegen=true) → rebuild (true). A probe error
//     (server down / timing out) is best-effort: WARN and do NOT rebuild —
//     rebuilding against a down/timing-out server is futile, so keep the existing
//     resident and wait for a successful probe rather than storm a from-scratch
//     rebuild on a transient failure.
//  3. healthy (segments present AND covering enough) → no rebuild (false).
func (c *client) healNeedsRebuild(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	// ONE manifest snapshot per heal pass: the presence probe (HasShippedFromSnapshot)
	// and the coverage probe (segmentPoolDegenerate → ShippedDocCountFromSnapshot) both
	// derive from this single List(0) instead of each issuing their own.
	snapshot, err := c.segmentMgr.ShippedManifestSnapshot(ctx, gt, name)
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
		// Healthy: segments present AND covering enough of the embedded corpus (or
		// the coverage signal is unknown/below-floor — disarm conservatively rather
		// than churn). No rebuild.
		return false, nil
	}
	// Degenerate-but-nonzero pool: the shipped corpus is present but the live
	// searchable pool is below coverage. Before paying a from-scratch rebuild, try a
	// one-shot read-engine load of the intact persisted corpus.
	resDegen, rerr := c.segmentMgr.ReconcileResidentDegenerate(ctx, gt, name)
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
		// Load restored coverage — the intact corpus is now resident. Skip the rebuild.
		slog.Info("bootstrap: auto-heal restored degenerate pool via read-engine load (no rebuild)",
			"graph_type", gt, "name", name)
		return false, nil
	}
	// The load could NOT restore coverage — the genuinely missing/collapsed case.
	return true, nil
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
	covered, anyUnknown := c.segmentMgr.ShippedDocCountFromSnapshot(snapshot)
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
