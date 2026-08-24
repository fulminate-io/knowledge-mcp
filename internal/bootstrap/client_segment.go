// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentCacheDirFor is the L2 disk-cache root for client-built/pulled HNSW
// segment blobs, rooted at <root>/segments — where root is the already
// tilde-expanded --graph-storage data root the daemon was started with. That is
// the SAME expression the server resolves its own segment store under
// (filepath.Join(<graph-storage>, "segments")), so client L2 and server store
// co-locate over a shared root — the successor to the retired HOME-fixed
// segmentCacheDir() that unconditionally returned <home>/.knowledge/segments.
//
// When a Fulminate account is selected the root is PARTITIONED BY ACCOUNT
// (accountSegmentRoot), so two accounts' blobs for the same graph name can
// never occupy the same directory. With no selection the path is unchanged.
func segmentCacheDirFor(root string) string {
	return accountSegmentRoot(root, selectedAccountForSegments())
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
// residencyBudgetBytes is the RESIDENT BLOB BYTE ceiling every per-graph segment
// pool shares before the coldest are unloaded (segmentdist manager_residency.go);
// 0 disables eviction entirely. It comes from
// --segment-residency-budget-bytes / KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES.
func (c *client) ensureSegmentManager(graphStorage string, residencyBudgetBytes int64) {
	if c.router != nil && c.segmentMgr == nil {
		// WithSegmentTransport supplies the lazy agent /v1/segments control transport
		// (c.buildCloudSyncTransport → *auth.Transport) so the source factory selects the
		// GCS segment source on the logged-in cloud path; a not-logged-in / OSS caller
		// still gets the L2-local source, and a logged-in caller whose transport build
		// fails falls back to RPC. The transport shares the daemon's SINGLE cloud token
		// source (one warm source, ~one refresh per token lifetime) instead of minting a
		// fresh cold keychain source per manager. The closure adapts the *auth.Transport
		// return to the segmentdist.SegmentControlTransport factory type (Go has no
		// return-type covariance, so the bare method value is not assignable).
		// WithGraphAdmitter records the search side of the working set: a user
		// search IS the direct interaction that admits a graph, and it is the
		// one admission that does not pass through Router.Execute.
		c.segmentMgr = segmentdist.NewManager(c.router, segmentCacheDirFor(graphStorage), 0,
			segmentdist.WithSegmentTransport(func() (segmentdist.SegmentControlTransport, error) {
				return c.buildCloudSyncTransport()
			}),
			segmentdist.WithGraphAdmitter(func(gt kgtypes.GraphType, name string) {
				c.AdmitGraph(gt, name, "search")
			}),
			// WithResidencyBudget bounds the RESIDENT HEAP BYTES every per-graph pool
			// occupies together; crossing it unloads the coldest pools, which reload
			// from the local L2 disk cache on their next CONSUMER touch. 0 disables
			// eviction entirely. Arming it is safe only because every background arm
			// declines an evicted pool and every ArmVerdict consumer branches on
			// Evicted — without those, the next reconcile tick would resurrect each
			// eviction at full network cost and the local heal decider would read an
			// evicted pool's resident==0 as a reason to rebuild from scratch.
			segmentdist.WithResidencyBudget(residencyBudgetBytes))
	}
}

// PoolEvicted reports whether EITHER format's segment pool for this graph is
// currently evicted from memory by the residency budget. It is THE client-side
// residency predicate: the heal decider, the repair arm and the manage(status)
// coverage band all read this one method rather than deriving their own answer.
//
// A client with no Manager returns false, which is correct rather than a default:
// such a client has no pools, so none of them are evicted.
//
// WHY DECIDERS NEED IT. An evicted pool reports a residency count of ZERO, and a
// decider that reads that zero as evidence about the CORPUS concludes the graph is
// uncovered — handing it to a full rebuild or a corpus-scale re-ship, on the
// strength of a measurement nobody took. Consulting this first is what turns that
// into a decline.
func (c *client) PoolEvicted(gt kgtypes.GraphType, name string) bool {
	if c == nil || c.segmentMgr == nil {
		return false
	}
	return c.segmentMgr.PoolEvicted(gt, name)
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
	return segmentCoverageAdapter{mgr: c.segmentMgr}
}

// segmentCoverageAdapter is the ONE place the backstop's record crosses from the
// segment package into the tools carrier.
//
// IT EXISTS BECAUSE THE IMPORT CANNOT GO THE OTHER WAY: the segment package's own
// in-package tests import tools, so tools may not import the segment package in
// production, and therefore cannot name segmentdist.RepairState. bootstrap is the
// composition root and already imports both, so the conversion belongs here rather
// than inverting either layer. Every other method is a straight pass-through.
type segmentCoverageAdapter struct{ mgr *segmentdist.Manager }

func (a segmentCoverageAdapter) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (int, bool, error) {
	return a.mgr.ShippedSegmentDocCount(ctx, gt, name)
}

func (a segmentCoverageAdapter) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.ResidentDocCount(gt, name)
}

// LoadRebuildState / LoadMergeWatermark forward this client's own consumer
// positions, which the status row renders as "how long since each advanced".
// Both are local record reads on the Manager — no RPC.
func (a segmentCoverageAdapter) LoadRebuildState(
	gt kgtypes.GraphType, name string,
) (int64, []searchengine.ExternalID, error) {
	return a.mgr.LoadRebuildState(gt, name)
}

func (a segmentCoverageAdapter) LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error) {
	return a.mgr.LoadMergeWatermark(gt, name)
}

func (a segmentCoverageAdapter) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.LiveResidentDocCount(gt, name)
}

func (a segmentCoverageAdapter) RepairVerification(
	gt kgtypes.GraphType, name string,
) (tools.RepairVerification, bool) {
	st, ok := a.mgr.RepairStateCached(gt, name)
	if !ok {
		return tools.RepairVerification{}, false
	}
	return tools.RepairVerification{
		Residue:         st.Residue,
		Converged:       st.Converged,
		Scanned:         st.Scanned,
		VerifiedAtNanos: st.VerifiedAtNanos,
	}, true
}

// SegmentShipper returns the SAME *segmentdist.Manager as the stage-then-finalize SHIP
// surface the rebuild_segments driver drives (StageRebuildPartition / FinalizeRebuild /
// InvalidateLocal). Returns an UNTYPED nil
// interface (not a typed nil adapter) when the pipeline was not wired, so the
// driver's nil-guard fires correctly.
//
// It is wrapped rather than returned bare because ONE method's result crosses the
// package boundary as a tools-local type: the delta finalize reports three coupled
// facts, and segmentdist cannot name tools.RebuildDeltaResult (nor tools segmentdist).
// The adapter EMBEDS the Manager, so every other method of the seam is the Manager's
// own — the same vocabulary-mapping shape segmentPrunerAdapter uses below.
func (c *client) SegmentShipper() tools.SegmentShipper {
	if c.segmentMgr == nil {
		return nil
	}
	return segmentShipperAdapter{Manager: c.segmentMgr}
}

// segmentShipperAdapter is the ONLY place the tools-local and segmentdist-native
// delta-result vocabularies meet. Embedding keeps it to exactly one translated
// method; adding a method to the seam that the Manager satisfies directly needs no
// change here.
type segmentShipperAdapter struct{ *segmentdist.Manager }

// FinalizeRebuild maps the Manager's reset-finalize result onto the tools-local
// struct. The values are carried across UNCHANGED; the two vocabularies exist only
// because neither package may import the other.
func (a segmentShipperAdapter) FinalizeRebuild(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (tools.RebuildFinalizeResult, error) {
	res, err := a.Manager.FinalizeRebuild(ctx, gt, name)
	return tools.RebuildFinalizeResult{
		HNSWSuperseded: res.HNSWSuperseded,
		BM25Superseded: res.BM25Superseded,
		Swapped:        res.Swapped,
	}, err
}

// ReEmitRebuiltDelta maps the Manager's three coupled return values onto the
// tools-local result struct. The values are carried across UNCHANGED — the struct
// exists to keep the seam at one method, not to reinterpret anything.
func (a segmentShipperAdapter) ReEmitRebuiltDelta(
	ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
) (tools.RebuildDeltaResult, error) {
	swapped, applicable, derived, err := a.Manager.ReEmitRebuiltDelta(ctx, gt, name, hnswDocs, bm25Docs)
	return tools.RebuildDeltaResult{
		Swapped:            swapped,
		Applicable:         applicable,
		DerivedBucketCount: derived,
	}, err
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

// SegmentDeleter returns the seam that carries a delete into the shipped segment
// corpus, wrapping the SAME *segmentdist.Manager the client holds. Returns an
// UNTYPED nil interface (not a typed nil *Manager) when the segment manager was not
// constructed, so a caller's nil-check fires correctly and the delete simply skips
// its re-emit — the same best-effort disposition a failed re-emit gets.
//
// No adapter is needed: the seam's only method takes already-imported kgtypes and
// searchengine types, so *segmentdist.Manager satisfies it directly.
func (c *client) SegmentDeleter() tools.SegmentDeleter {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
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

// ClearHealLatch clears the per-(graphType, name) auto-heal breaker latch — the
// manual rebuild_segments re-arm exposed via the tools.ClientDeps seam
// (handleClientRebuildSegments calls it on a scanned>0 success). Delegates to the
// healBreaker (zero-value usable), so it is safe on a test-built *client.
func (c *client) ClearHealLatch(gt kgtypes.GraphType, name string) {
	c.healBreaker.ClearHealLatch(gt, name)
}

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
//     below tools.CoverageRatioThreshold × the graph's embedded-node count, once the
//     embedded count clears tools.SegmentCoverageFloor) → try a one-shot read-engine
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
			// Background auto-heal with no originating tool call — it stamps its
			// own query-origin operation, and separately from the periodic
			// coverage reconcile so a heal storm is distinguishable from routine
			// reconciliation in the metrics.
			ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentHeal)
			// Breaker gate: a graph latched disarmed after healBreakerTripThreshold
			// no-progress rebuilds stops firing. Return ErrHealDisarmed so the collector
			// latches its own healDisarmed flag and stops re-arming this closure per wake.
			if !c.healBreaker.Allow(gt, name) {
				return pipeline.ErrHealDisarmed
			}
			needsRebuild, err := c.healNeedsRebuild(ctx, gt, name)
			if err != nil {
				return err
			}
			if !needsRebuild {
				// Healthy pool, a degenerate pool a read-engine load already restored to
				// coverage, or a BM25 arm the gate declined — no from-scratch rebuild.
				return nil
			}
			// Zero shipped segments OR a degenerate pool a read-engine load could not
			// restore: heal by rebuilding from the already-embedded nodes. Reuse the
			// SAME login-routed scanner seam the manual op uses (the accessor carries
			// the c.router==nil guard) and the SAME segment manager as shipper.
			// RebuildSegments owns the single-flight shared with the manual op.
			// FROM SCRATCH: the heal fires precisely because the shipped corpus is
			// absent or degenerate, so it needs the whole corpus rebuilt — scoping the
			// scan to what changed recently would rebuild a slice of a corpus that is
			// missing, and the heal would never converge.
			out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), gt, name, true)
			if err != nil {
				return err
			}
			// published is logged beside the counts because they answer different
			// questions: the counts say what was BUILT AND SHIPPED, published says
			// whether the manifest swap that makes those blobs the live set LANDED. A
			// heal that ships everything and publishes nothing restored no coverage, and
			// a line reading only the counts calls that a successful heal.
			slog.Info("bootstrap: auto-heal rebuilt segments after read-engine load could not restore coverage for builtin graph",
				"graph_type", gt, "name", name, "ran", out.Ran, "scanned", out.Scanned, "built", out.Built,
				"partial", out.Partial, "hnsw_pruned", len(out.HNSWPruned), "bm25_pruned", len(out.BM25Pruned),
				"published", out.Published)
			// A COMPLETED rebuild (ran, with the error path already returned above)
			// consumes the BM25 arm's no-progress shot, then classifies against the
			// breaker — scanned==0, or no shipped-completeness gain, is no-progress.
			if out.Ran {
				c.armBM25HealProgress(gt, name)
			}
			c.classifyHealOutcome(ctx, gt, name, out.Ran, out.Scanned, out.Built, out.Partial)
			return nil
		}
	}
}
