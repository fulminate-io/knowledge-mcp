// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentCacheDirFor is the L2 disk-cache root for client-built HNSW segment
// blobs, rooted at <root>/segments — where root is the already tilde-expanded
// --graph-storage data root the daemon was started with. It is the client's ONLY
// segment store: nothing else reads or writes this tree.
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
// HOME-fixed path. Under a manually-split client/server --graph-storage the cache
// follows the CLIENT's root. For the default root (~/.knowledge/, tilde-expanded by
// the client to <home>/.knowledge) the location is <home>/.knowledge/segments.
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
		// WithGraphAdmitter records the search side of the working set: a user
		// search IS the direct interaction that admits a graph, and it is the
		// one admission that does not pass through Router.Execute.
		c.segmentMgr = segmentdist.NewManager(segmentCacheDirFor(graphStorage), 0,
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

// COMPILE-TIME PROOF THAT *client CARRIES THE LOADING DECIDER the tools layer's
// optional seam type-asserts for. That seam (loadLiveResidentReader) is unexported in
// tools and resolved by a type assertion, so a drift in this method's SHAPE would not
// break the build — it would silently make the assertion fail, and
// practiceSegmentGapNotice would answer every zero-hit practice search with its
// "probe seam is unwired" caveat forever. A permanently-taken decline branch reads
// exactly like a working one. This line is what turns that into a build error.
var _ interface {
	LoadLiveResidentDocCount(context.Context, kgtypes.GraphType, string) (int, error)
} = (*client)(nil)

// LoadLiveResidentDocCount is the DECIDER half of the live-resident read, exposed on
// the client so the tools layer's optional loadLiveResidentReader seam resolves.
//
// IT IS THE LOADING VARIANT ON PURPOSE. The reporter (LiveResidentDocCount) takes no
// load and legitimately reads 0 for a graph whose engine has not loaded yet, so a
// caller qualifying a zero-hit search with it would announce "the ranked index is
// missing" about a pool that is merely cold. This one loads first and returns its
// load error rather than swallowing it, so a caller that could not load declines
// instead of acting on an empty view.
//
// A CLIENT WITH NO MANAGER RETURNS AN ERROR, not zero — the opposite disposition from
// PoolEvicted above, and for the same underlying reason. "Not evicted" is a TRUE
// statement about a client that runs no residency budget; "zero live-resident
// documents" is NOT a true statement about a client that has no engine to ask, it is
// an inability to measure. Returning zero here would let a caller qualify a zero-hit
// search as a missing index on the strength of a read nobody performed.
//
// CALLERS MUST STILL FENCE ON EVICTION FIRST. This deliberately loads, so calling it
// on an evicted pool would re-materialize the pool and undo the residency decision.
func (c *client) LoadLiveResidentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	if c == nil || c.segmentMgr == nil {
		return 0, errors.New("bootstrap: no segment manager wired — the live-resident count cannot be measured")
	}
	return c.segmentMgr.LoadLiveResidentDocCount(ctx, gt, name)
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
) (int, error) {
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
// cloud, cicd, practice, checks — the SAME gate the manual rebuild_segments op
// uses (handleClientRebuildSegments), so the auto-heal arm and the manual rebuild
// gate cannot drift; the non-embeddable builtins (linkage, transformers) and the
// raw graphs (logs, web, pdf) have no segments to heal and get a nil closure. The
// closure runs on the armed embed drain edge: a CHEAP presence + coverage probe
// and, when a from-scratch rebuild is genuinely needed, the rebuild driver
// (single-flight, shared with the manual rebuild_segments op). A healthy graph
// (segments present AND covering enough of the embedded corpus) is a probe +
// disarm, never a churn.
//
// The probe makes a TWO-way decision, and the operands are LOCAL on both sides:
//  1. a LOST POOL on the HNSW arm — a resident doc count of zero against a non-empty
//     embedded corpus, at any magnitude (hnswPoolLost) — or, on the BM25 arm, the
//     ratio band that arm still carries → rebuild from scratch. The load happens FIRST
//     regardless: the observation probe cache-first load()s the intact persisted
//     corpus before the count is read, so a pool that was merely not-yet-resident — a
//     daemon restart whose searchable pool had not loaded yet — heals without a
//     rebuild.
//  2. anything else → probe + disarm, never a churn.
//
// THIS CLOSURE IS THE AWAY-FROM-QUIESCENCE ARM, AND ITS SILENCE IS DELIBERATE. It
// fires on the embed drain edge of ONE axis, which is not the same thing as the
// pipeline being quiescent — the summary axis may still be working. The EXACT balance
// verdict, which is where a partial shortfall is now detected, is formed by the
// separate quiescence closure (buildBalanceFactory) that runs only once BOTH axes are
// drained at the current collect epoch. So this arm asserting nothing but a lost pool
// is not a coverage gap; it is the half of the split that can be answered honestly
// while the corpus is still moving.
//
// AN EVICTED POOL IS DECLINED AHEAD OF BOTH, because its resident count reads zero
// while every byte is still on disk: rebuilding it would undo the eviction at the
// highest possible cost.
//
// THE CONSERVATIVE-UNKNOWN GUARD IS GONE WITH THE CONDITION IT GUARDED. It disarmed
// the ratio whenever a shipped segment reported doc_count==0 — a blob predating the
// doc_count wire plumbing, whose real coverage was therefore unknowable — so that a
// fleet mid-migration would not read covered=0 on every healthy graph and storm a
// fleet-wide rebuild. The numerator is now the LOCAL resident doc count, which the
// engine computes from what it actually imported and which is never the
// pre-doc_count sentinel, so there is no unknown state left to be conservative about.
func (c *client) buildHealFactory() func(kgtypes.GraphType, string) func(context.Context) error {
	return func(gt kgtypes.GraphType, name string) func(context.Context) error {
		// Rebuildable-segments gate FIRST — the auto-heal closure is built only for
		// graphs that carry rebuildable segments (the embeddable builtins: knowledge,
		// code, cloud, cicd, practice, checks). This is the SAME kgtypes.HasRebuildableSegments
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
			c.classifyHealOutcome(gt, name, out.Ran, out.Scanned)
			return nil
		}
	}
}
