// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// reconcileOneGraph is the reconcile pass's PER-GRAPH DECISION CASCADE, lifted out
// of the enumeration loop so each reads as the one thing it is: the caller decides
// WHICH graphs to walk, this decides WHAT to do with one.
//
// It is a sequence of gates, and the ORDER of them is the design. Every early return
// below is a reason not to reach the arm after it, and the comment at each one says
// which reason — read top to bottom, the function is the pass's policy.
//
// Best-effort throughout, exactly as the enclosing pass is: every arm logs its own
// failure and the cascade continues or returns, because one graph's failure must
// never stop the sweep over the others.
func (c *client) reconcileOneGraph(ctx context.Context, g segmentGraphRef, deltaScope map[segmentGraphRef]struct{}) {
	// Pull this graph's delta window FIRST and land both its halves: the deletes, so
	// one learned this pass is already out of its routed buckets when the re-emit
	// below ships them, and the live items a co-worker changed, so the drain below
	// ships the partitions this merge dirtied. Draining first would ship the
	// partitions once carrying the dead document and again on the next pass without
	// it, and would defer every merged document a full tick.
	pending := c.consumeSegmentDelta(ctx, g, deltaScope)
	// Clear the record's tombstone for any id a WRITE has re-created, before the drain
	// below reads that record to build its filter. Without this the drain drops the
	// fresh document of a deleted-then-re-created id and the entry is GONE — unlike a
	// seeded-dead import, which the next re-emit of its partition repairs.
	//
	// BOTH NEIGHBORS ARE LOAD-BEARING. It runs AFTER the delta consume because that
	// pass can legitimately re-add the id from a window that still contains its delete,
	// and BEFORE the drain because the drain reads the tombstone set once, at its head,
	// for the filter both formats share.
	c.untombstoneRecreatedWrites(g)
	// Drain the graph's write backlog FIRST — this is the only thing that calls
	// it, and the early returns below skip everything after them. The healthy-graph
	// return is the one that matters: a graph holding unpublished writes is normally
	// HEALTHY, so a drain placed after it would never run for exactly the graphs
	// that have work. Best-effort, because one graph's failure must not stop the
	// pass from reconciling the others.
	//
	// IT IS ALSO PART ONE OF THE MERGE'S TWO-PART COMMIT. The horizon advances only on
	// the branch where this drain succeeded, so a skipped drain leaves the horizon
	// where it was and the same window is re-pulled next tick — idempotent, because
	// the merge's add is keyed by id.
	if err := c.segmentMgr.ReEmitDirtyBuckets(ctx, g.gt, g.name); err != nil {
		slog.Warn("bootstrap: segment re-emit failed (continuing)",
			"graph_type", g.gt, "name", g.name, "error", err)
	} else {
		c.commitMergeWatermark(g, pending)
	}
	// CONVERGE THE L2 CACHE TO THE PUBLISHED MANIFEST, before the degeneracy probe
	// below. This is where the L2-first load's completeness hole is repaired: a
	// cache holding only the blobs the last rebuild ADDED pins the engine to that
	// subset, and load() accepts it because any non-empty cache satisfies it.
	//
	// ORDER MATTERS AND IT IS NOT COSMETIC. A graph healed here is no longer
	// degenerate, so the probe below does not flag it and no PG rebuild fires for a
	// shortfall a fetch already fixed. Running it after would spend a full rebuild
	// to reach the state this call reaches with a diff fetch.
	//
	// Best-effort: its own arms log the detail, so one graph's failure never stops
	// the pass. It is gated internally on a LOCAL comparison, so a healthy graph
	// pays no network here.
	if err := c.segmentMgr.ReconcileManifestCompleteness(ctx, g.gt, g.name); err != nil {
		slog.Warn("bootstrap: segment manifest-completeness reconcile failed (continuing)",
			"graph_type", g.gt, "name", g.name, "error", err)
	}
	// RE-BUCKET TRIGGER: the graph's resident layout may be a FULL DOUBLING behind
	// the partition count its corpus now derives. That crossing is one the delta
	// path structurally cannot close — a delta rebuild is always scoped to the
	// partitions a write reached, and a graph that grew and then went quiet has no
	// writes left to carry it the rest of the way.
	//
	// PLACED HERE, AND BOTH NEIGHBORS MATTER. It runs AFTER the completeness
	// converge above, because a resident set still short for CACHE reasons inflates
	// the derived count against the observed segment count and could fire a rebuild
	// the converge would have made unnecessary. It runs BEFORE the degeneracy probe
	// below, because the healthy-graph `continue` there ends the iteration for
	// exactly the graphs this exists to serve — behind, but perfectly healthy — so a
	// detector placed after it would never run for them at all.
	//
	// The detection itself is two local reads per format: no source access, no lock
	// beyond the engine's own snapshot load.
	if candidate, current, needed := c.segmentMgr.ReBucketNeeded(g.gt, g.name); needed {
		// The SAME breaker the degenerate-graph path uses below. A crossing whose
		// reset repeatedly fails to land would otherwise retry every tick forever,
		// and the breaker already encodes that no-progress policy — a second bound
		// beside it would be two policies to keep in agreement. Allow is a pure read,
		// so consulting it costs the trigger nothing.
		if c.healBreaker.Allow(g.gt, g.name) {
			c.rebuildReBucketGraph(ctx, g.gt, g.name, candidate, current)
		} else {
			slog.Debug("bootstrap: segment reconcile — re-bucket crossing found but the auto-heal breaker is latched, skipping rebuild",
				"graph_type", g.gt, "name", g.name,
				"derived_bucket_count", candidate, "observed_segment_count", current)
		}
	}
	// COVERAGE-REPAIR ARM: the graph may be fully embedded and perfectly healthy by
	// every probe below, yet missing documents from its searchable corpus because the
	// ship that would have carried them was swallowed or lost with the process.
	//
	// PLACED HERE FOR THE SAME REASON THE RE-BUCKET TRIGGER IS. The `if !degenerate`
	// return below ends the iteration for exactly the graphs this serves — short, but
	// not degenerate — so an arm placed after it would never run for them. It runs
	// AFTER the completeness converge above so a resident set still short for CACHE
	// reasons is not read as a coverage gap.
	//
	// IT IS A BACKSTOP RATHER THAN THE FRESHNESS PATH — the delta merge above is that
	// — so it runs only on a PERIODIC sweep, which is what deltaScope == nil means
	// here. A nudge-woken pass returns at its very first gate for every graph, keeping
	// the expensive arm off the interactive path.
	//
	// It costs nothing for a graph a persisted converged record already answers for,
	// nothing for one that does not hold this tick's round-robin slot, and two reads
	// for one that does and is converged.
	c.repairUncoveredGraph(ctx, g, deltaScope == nil)
	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, g.gt, g.name)
	if err != nil {
		slog.Warn("bootstrap: segment reconcile probe failed (continuing)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return
	}
	if !degenerate {
		return // healthy (or disarmed) — no rebuild.
	}
	// reconcile diagnostic (kept per keep-debug-logging): on the degenerate branch
	// record the SHIPPED-corpus doc count vs the EMBEDDED node count. When shipped
	// covers embedded, the read engine is flagged degenerate only because it has
	// not loaded the intact corpus — a PG RebuildSegments writes the DETERMINISTIC
	// engine (not this read engine), so it cannot raise the resident count the
	// probe re-reads and the rebuild is wasted. The healNeedsRebuild gate below
	// acts on exactly this shipped-vs-embedded signal; this line makes it
	// observable per tick.
	if snapshot, serr := c.segmentMgr.ShippedManifestSnapshot(ctx, g.gt, g.name, hnsw.New().Name()); serr != nil {
		slog.Debug("bootstrap: segment reconcile degenerate-branch shipped probe failed",
			"graph_type", g.gt, "name", g.name, "error", serr)
	} else {
		shippedDocs, anyUnknown := c.segmentMgr.ShippedDocCountFromSnapshot(snapshot, hnsw.New().Name())
		embedded, eerr := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), g.gt, g.name)
		slog.Debug("bootstrap: segment reconcile degenerate branch",
			"graph_type", g.gt, "name", g.name,
			"shipped_docs", shippedDocs, "any_unknown", anyUnknown,
			"embedded", embedded, "embedded_err", eerr)
	}
	// SHIPPED-COMPLETENESS GATE: ReconcileResidentDegenerate above flags
	// when the READ engine is below the SHIPPED corpus — which a merely lazily-loaded
	// read engine trips even when the shipped corpus is COMPLETE.
	//
	// THE ORIGINAL LOOP CAUSE IS STRUCTURALLY GONE, and the gate is kept anyway. A PG
	// RebuildSegments used to write a SECOND, deterministic engine, so it could never
	// raise the read engine's resident count and the next 5-min tick re-flagged and
	// rebuilt again — the ~85 rebuilds/wk loop. The reset now swaps its layer into the
	// engine this probe reads, so a landed rebuild does raise that count and the loop
	// cannot recur for that reason. The gate still earns its place: it stops an
	// expensive full rebuild whenever the shipped corpus is already complete and the
	// read engine is merely cold, which is the common case and independent of how many
	// engines exist. healNeedsRebuild asks
	// the RIGHT question for a PG regen — is the SHIPPED/L2 corpus genuinely
	// incomplete vs the embedded node count? (HasShippedFromSnapshot +
	// segmentPoolDegenerate, then a read-engine-load attempt) — returning true ONLY
	// when the corpus is genuinely zero/incomplete AND a load cannot restore it. So
	// the expensive PG rebuild fires on genuine incompleteness, never on a lazy read
	// engine. The ReconcileResidentDegenerate call above is still made FIRST so its
	// warm-load side effect (load()+recoverIfDegenerate) is preserved. healNeedsRebuild
	// re-probes ReconcileResidentDegenerate internally on this rare degenerate branch
	// (a second cheap load/List, off the bind path); healthy graphs short-circuited at
	// `if !degenerate` above and never reach it. The MANUAL manage(rebuild_segments)
	// path (handleClientRebuildSegments) is intentionally NOT gated — an operator
	// asking for a rebuild always gets one.
	//
	// SCOPE (boot herd + hypothesis c): this collapses the STEADY-STATE 5-min re-flag
	// loop, not a boot-time one-rebuild-per-daemon herd — the RebuildSegments
	// single-flight is PER-PROCESS, so N cold fleet daemons can each pay one rebuild
	// until a complete corpus is shipped+visible. That is also how the gate mitigates
	// c: once ANY daemon ships a complete corpus, every other daemon's healNeedsRebuild
	// sees shipped-complete and skips. The residual boot rebuild is within the ticket
	// goal — post-deploy observers must not read it as a regression.
	needsRebuild, herr := c.healNeedsRebuild(ctx, g.gt, g.name)
	if herr != nil {
		slog.Warn("bootstrap: segment reconcile shipped-completeness gate failed (continuing, no rebuild)",
			"graph_type", g.gt, "name", g.name, "error", herr)
		return
	}
	if !needsRebuild {
		slog.Debug("bootstrap: read pool degenerate but shipped corpus complete — skipping PG rebuild (load retry)",
			"graph_type", g.gt, "name", g.name)
		return
	}
	// Heal breaker gate: once a graph has latched disarmed after
	// healBreakerTripThreshold no-progress rebuilds, skip the FUTILE RebuildSegments.
	// The recovery probe (ReconcileResidentDegenerate) above still ran — only the
	// rebuild is gated — so the legitimate ~5-min recovery path keeps working.
	if !c.healBreaker.Allow(g.gt, g.name) {
		slog.Debug("bootstrap: segment reconcile — auto-heal breaker latched for graph, skipping rebuild (recovery probe still ran)",
			"graph_type", g.gt, "name", g.name)
		return
	}
	c.rebuildDegenerateGraph(ctx, g.gt, g.name)
}

// rebuildDegenerateGraph runs the heal rebuild for ONE graph the reconcile pass has
// already decided is genuinely incomplete, then reports and classifies the outcome.
// It is the tail of reconcileOneGraph's cascade above, lifted out so that cascade
// reads as the sequence of decisions it is; every early `return` there is a reason
// NOT to reach this.
//
// A rebuild failure is logged and swallowed rather than propagated: this is a
// periodic best-effort pass over every graph, and one graph's failure must not stop
// the sweep.
//
// FROM SCRATCH, always. This arm runs because the shipped corpus is incomplete, so
// the slice of it that changed since the last rebuild is exactly the wrong scope:
// rebuilding only that leaves the rest as missing as it was, and the heal never
// converges. It goes through the PROVEN rebuild path (single-flight shared with the
// manual op + embed-drain auto-heal — NOT a bare load).
func (c *client) rebuildDegenerateGraph(ctx context.Context, gt kgtypes.GraphType, name string) {
	out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), gt, name, true)
	if err != nil {
		slog.Warn("bootstrap: segment reconcile rebuild failed (continuing)",
			"graph_type", gt, "name", name, "error", err)
		return
	}
	// published distinguishes a pool this pass actually replaced from one whose
	// rebuilt blobs shipped and were then refused at the manifest swap. Both return
	// a nil error, and only the first restored the degenerate pool this arm exists
	// to fix.
	slog.Info("bootstrap: segment reconcile rebuilt a degenerate live pool",
		"graph_type", gt, "name", name,
		"ran", out.Ran, "scanned", out.Scanned, "built", out.Built,
		"partial", out.Partial, "hnsw_pruned", len(out.HNSWPruned), "bm25_pruned", len(out.BM25Pruned),
		"published", out.Published)
	// A COMPLETED rebuild (ran; the error path returned above) consumes the BM25
	// arm's no-progress shot — a no-op when this pass never consulted that gate.
	if out.Ran {
		c.armBM25HealProgress(gt, name)
	}
	// Classify against the breaker (records ONLY on ran==true) — the same strict
	// no-progress/progress rule the embed-drain trigger uses.
	c.classifyHealOutcome(ctx, gt, name, out.Ran, out.Scanned, out.Built, out.Partial)
}

// rebuildReBucketGraph runs the one-time reset rebuild for ONE graph whose resident
// layout the pass has found a FULL DOUBLING behind the partition count its corpus
// derives, then reports and classifies the outcome exactly as the heal arm does.
//
// IT IS A SIBLING OF rebuildDegenerateGraph RATHER THAN A SECOND CALLER OF IT, and
// the log line is the whole difference. Reusing that function would announce
// "rebuilt a degenerate live pool" for a graph that is not degenerate at all, and a
// line that misattributes the cause is worse than a second small function: it sends
// the operator looking for a collapse that never happened. So this line names the
// crossing and carries both operands.
//
// FROM SCRATCH, always, through the SAME single-flighted rebuild path the manual op
// and the heal share — so a triggered reset cannot race either of them. A
// delta-scoped rebuild is exactly the wrong scope here: the count is derived from
// the FULL corpus, and a rebuild that never assembles the full corpus re-derives
// nothing.
//
// THE LATCH NEEDS NO STATE. The from-scratch rebuild stages one partition per hash
// bucket and swaps the layer in whole, so once it lands the resident segment count
// equals the count the corpus derives and the detector reads false on the next tick.
// If it does NOT land — a coverage refusal or a 409, both nil-error skips — the gate
// stays open deliberately and the next tick retries, which is the right behavior
// for work that has not happened yet. That retry is what the breaker bounds.
//
// A rebuild failure is logged and swallowed, like every other arm of this
// best-effort pass: one graph's failure must not stop the sweep.
func (c *client) rebuildReBucketGraph(ctx context.Context, gt kgtypes.GraphType, name string, candidate, current int) {
	out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), gt, name, true)
	if err != nil {
		slog.Warn("bootstrap: segment reconcile re-bucket rebuild failed (continuing)",
			"graph_type", gt, "name", name,
			"derived_bucket_count", candidate, "observed_segment_count", current, "error", err)
		return
	}
	slog.Info("bootstrap: segment reconcile re-bucketed a graph whose layout was a full doubling behind its corpus",
		"graph_type", gt, "name", name,
		"derived_bucket_count", candidate, "observed_segment_count", current,
		"ran", out.Ran, "scanned", out.Scanned, "built", out.Built,
		"partial", out.Partial, "hnsw_pruned", len(out.HNSWPruned), "bm25_pruned", len(out.BM25Pruned),
		"published", out.Published)
	// A COMPLETED rebuild consumes the BM25 arm's no-progress shot, and the outcome is
	// classified against the same breaker this arm consulted before firing — the same
	// strict no-progress/progress rule the heal and embed-drain triggers use.
	if out.Ran {
		c.armBM25HealProgress(gt, name)
	}
	c.classifyHealOutcome(ctx, gt, name, out.Ran, out.Scanned, out.Built, out.Partial)
}

// untombstoneRecreatedWrites clears the persisted record's tombstone for every id that
// has a live document queued again, so the drain that follows does not filter it out.
//
// It takes no context because neither call it makes takes one. Best-effort like every
// other arm of the cascade: a failure here costs this pass's re-creations and nothing
// else, and the next tick tries again.
func (c *client) untombstoneRecreatedWrites(g segmentGraphRef) {
	ids := c.segmentMgr.TombstonedPendingWriteIDs(g.gt, g.name)
	if len(ids) == 0 {
		return
	}
	cleared, err := tools.UntombstoneWrittenIDs(c.SegmentShipper(), g.gt, g.name, ids)
	if err != nil {
		slog.Warn("bootstrap: could not clear tombstones for re-created ids (continuing; the drain will still drop their writes this pass)",
			"graph_type", g.gt, "name", g.name, "ids", len(ids), "error", err)
		return
	}
	if cleared > 0 {
		slog.Info("bootstrap: cleared tombstones for ids that were written again after being deleted",
			"graph_type", g.gt, "name", g.name, "cleared", cleared)
	}
}
