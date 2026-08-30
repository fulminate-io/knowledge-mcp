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
	// THE DEGENERACY VERDICT IS COMPUTED HERE, from the per-format observations
	// plus the embedded denominator this layer holds. It used to be one call to a
	// Manager wrapper that OR'd a per-arm Degenerate field; that field went with
	// the shipped doc count behind it, so the wrapper had nothing
	// left to compute and the verdict moved to the layer holding its operand.
	//
	// ONE DENOMINATOR READ SERVES EVERY ARM: it is per-GRAPH while the resident
	// numerators are per-FORMAT.
	obs, err := c.segmentMgr.ResidentObservationsByFormat(ctx, g.gt, g.name)
	if err != nil {
		slog.Warn("bootstrap: segment reconcile probe failed (continuing)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return
	}
	embedded, eerr := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), g.gt, g.name)
	if eerr != nil {
		slog.Warn("bootstrap: segment reconcile embedded-count read failed (continuing)",
			"graph_type", g.gt, "name", g.name, "error", eerr)
		return
	}
	hnswFormat := hnsw.New().Name()
	degenerate := false
	for _, o := range obs {
		// AN EVICTED OR UNREADABLE ARM CONTRIBUTES NOTHING. Both report
		// ResidentAfterLoad 0, which either predicate below would read as a lost
		// pool and turn into a from-scratch rebuild — on the strength of a measurement
		// nobody took. An evicted pool re-materializes on its next consumer search;
		// rebuilding it from scratch would undo the eviction at the highest cost.
		if o.Evicted || o.Err != nil {
			continue
		}
		// THE TWO ARMS NOW ASK DIFFERENT QUESTIONS, so this loop routes per format
		// instead of applying one predicate to both.
		//
		// HNSW: the ratio band is RETIRED for this arm. THIS SWEEP IS NOT THE QUIESCENCE
		// EDGE — it is a periodic pass that runs whenever it runs, with no knowledge of
		// whether the pipeline is mid-drain — so it gets the away-from-quiescence
		// predicate, which asserts only a LOST POOL. The exact balance verdict is formed
		// at the drain edge, where the corpus is not moving underneath it. Applying an
		// exact zero-tolerance equation here would report every mid-convergence graph
		// unhealthy, which is the false-unhealthy this work removes.
		//
		// BM25: UNCHANGED, still the ratio band, because no exact BM25 corpus count
		// exists yet and deleting the band with no replacement would remove the only
		// detector for the collapse shape it was written for.
		if o.Format == hnswFormat {
			if hnswPoolLost(o.ResidentAfterLoad, embedded) {
				degenerate = true
			}
			continue
		}
		if degenerateAgainstEmbedded(o.ResidentAfterLoad, embedded) {
			degenerate = true
		}
	}
	if !degenerate {
		return // healthy — no rebuild.
	}
	// reconcile diagnostic (kept per keep-debug-logging): on the degenerate branch
	// record each arm's resident count against the EMBEDDED node count, which is the
	// comparison the verdict above actually made.
	for _, o := range obs {
		slog.Debug("bootstrap: segment reconcile degenerate branch",
			"graph_type", g.gt, "name", g.name, "format", o.Format,
			"resident_after_load", o.ResidentAfterLoad, "evicted", o.Evicted,
			"embedded", embedded, "arm_err", o.Err)
	}
	// COMPLETENESS GATE: the probe above flags when a READ engine is below the
	// embedded corpus — which a merely lazily-loaded read engine trips even when the
	// L2 corpus is COMPLETE.
	//
	// THE ORIGINAL LOOP CAUSE IS STRUCTURALLY GONE, and the gate is kept anyway. A
	// rebuild used to write a SECOND, deterministic engine, so it could never raise
	// the read engine's resident count and the next 5-min tick re-flagged and rebuilt
	// again — the ~85 rebuilds/wk loop. The reset now swaps its layer into the engine
	// this probe reads, so a landed rebuild does raise that count and the loop cannot
	// recur for that reason. The gate still earns its place: it stops an expensive
	// full rebuild whenever the L2 corpus is already complete and the read engine is
	// merely cold, which is the common case.
	//
	// healNeedsRebuild asks the RIGHT question for a regen — is the L2 corpus
	// genuinely incomplete against the embedded node count? — returning true ONLY
	// when a load cannot restore it. The observation probe above is still made FIRST
	// so its warm-load side effect is preserved. The MANUAL manage(rebuild_segments)
	// path (handleClientRebuildSegments) is intentionally NOT gated — an operator
	// asking for a rebuild always gets one.
	//
	// SCOPE (boot herd): this collapses the STEADY-STATE 5-min re-flag loop, not a
	// boot-time one-rebuild-per-daemon herd — the RebuildSegments single-flight is
	// PER-PROCESS, so N cold fleet daemons can each pay one rebuild until a complete
	// corpus is built. The residual boot rebuild is within the ticket goal —
	// post-deploy observers must not read it as a regression.
	// THE OPERANDS ARE REUSED, NOT RE-READ. obs and embedded were both resolved above
	// for this tick's verdict; healNeedsRebuildWith takes them so the gate does not
	// repeat an Engine Stats RPC and a two-engine observation probe that already ran
	// microseconds earlier. Reading them again is not merely wasteful — it would put
	// the reconcile path at three denominator RPCs per graph per tick, against a
	// per-graph denominator whose whole point is that one read serves every arm.
	needsRebuild, herr := c.healNeedsRebuildWith(ctx, g.gt, g.name, obs, embedded)
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
	// The observation probe above still ran — only the rebuild is gated — so the
	// legitimate ~5-min recovery path keeps working.
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
	c.classifyHealOutcome(gt, name, out.Ran, out.Scanned)
}

// rebuildBehindWindowGraph runs the from-scratch rebuild for ONE graph the server
// REFUSED to serve a delta window for, because the erasure journal was trimmed
// past this client's position.
//
// IT IS A SIBLING OF rebuildDegenerateGraph RATHER THAN A SECOND CALLER, for
// rebuildReBucketGraph's stated reason: reusing that function would announce
// "rebuilt a degenerate live pool" for a graph that is not degenerate, and a line
// that misattributes the cause sends the operator looking for a collapse that never
// happened. A behind-floor graph is not degenerate either — its pool is intact, its
// POSITION is unrecoverable.
//
// THE WATERMARK IS TWO SEPARATE CLAIMS, and both hold here.
// (a) THE REFUSED WINDOW'S HORIZON IS DISCARDED. This function never calls
// SaveMergeWatermark: the scan errored, so the caller returns a mergePending with
// Pull=false and commitMergeWatermark exits at its first guard. Nothing can commit
// a position derived from a window that was never served.
// (b) THE LANDED REBUILD'S HORIZON BECOMES THE POSITION. The from-scratch run
// publishes its own durable rebuild record, and mergeHorizonFor already reads that
// record as its third seed source — so the next window resolves from the rebuild's
// position with no new persistence path. If the rebuild does NOT land (breaker
// latched, or an error), the position is left where it was and the next pass is
// refused again, which is correct: an unrepaired client must keep being refused
// rather than quietly proceeding.
//
// THE BREAKER MATTERS MORE HERE THAN ON THE OTHER TWO ARMS. A refusal repeats on
// every pass until the position moves, so without it a graph whose rebuild keeps
// failing would rebuild every tick forever.
//
// IT FAILS LOUD. The enclosing pass is best-effort and this does not change that
// for the other arms — but a refusal detected and then swallowed is
// indistinguishable from a healthy pass, and its consequence is a deleted node that
// stays searchable forever. The WARN names the graph, this client's position, and
// on the SAME line whether the rebuild was attempted, skipped by the breaker, or
// failed. There is deliberately NO compensating partial-merge path: no state exists
// in which applying half a refused window is right.
func (c *client) rebuildBehindWindowGraph(ctx context.Context, g segmentGraphRef, since int64) {
	if !c.healBreaker.Allow(g.gt, g.name) {
		slog.Warn("bootstrap: segment delta REFUSED (behind the server's erasure retention floor) and the rebuild was SKIPPED — the auto-heal breaker is latched, so this graph stays behind and will be refused again next pass",
			"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "skipped-breaker-latched")
		return
	}
	out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), g.gt, g.name, true)
	if err != nil {
		slog.Warn("bootstrap: segment delta REFUSED (behind the server's erasure retention floor) and the recovery rebuild FAILED — this graph stays behind and will be refused again next pass",
			"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "failed", "error", err)
		return
	}
	slog.Warn("bootstrap: segment delta REFUSED (behind the server's erasure retention floor) — recovered with a from-scratch rebuild whose own horizon becomes this client's new position",
		"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "attempted",
		"ran", out.Ran, "scanned", out.Scanned, "built", out.Built,
		"partial", out.Partial, "published", out.Published)
	if out.Ran {
		c.armBM25HealProgress(g.gt, g.name)
	}
	c.classifyHealOutcome(g.gt, g.name, out.Ran, out.Scanned)
}

// rebuildUnreadableFloorGraph runs the ONE recovery rebuild for a graph whose delta
// pass declined because its retention floor could not be read.
//
// IT IS A SIBLING OF rebuildBehindWindowGraph RATHER THAN A SECOND CALLER OF IT, for
// the reason this file already states at rebuildReBucketGraph: every line of that
// arm says "behind the server's erasure retention floor", which is the wrong cause
// here and would send an operator hunting a refusal that never happened. The cause
// is local and unreadable state, and the remedy is a local path.
//
// WHY A RESET REBUILD CONVERGES THIS, by construction rather than by hope. A reset
// never reads the durable rebuild record at all — the load sits behind the non-reset
// branch — and the retention helper short-circuits on the zero a reset passes as its
// own position, so neither leg touches the unreadable record. On a landed publish the
// driver writes a FRESH record, and the next delta pass reads a healthy one.
//
// THE ATTEMPT IS CLAIMED ONCE PER GRAPH PER PROCESS. A second and later pass takes
// the honest decline path instead: see claimFloorRecovery for why an unwritable path
// would otherwise drive a full-corpus rebuild every pass forever.
func (c *client) rebuildUnreadableFloorGraph(ctx context.Context, g segmentGraphRef, since int64) {
	if !c.claimFloorRecovery(g) {
		slog.Warn("bootstrap: segment delta DECLINED (this client cannot read its own retention floor) and NO deletions were learned this pass — the recovery rebuild was already attempted this process, so fix the unreadable rebuild-state record under the segments cache directory",
			"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "already-attempted")
		return
	}
	if !c.healBreaker.Allow(g.gt, g.name) {
		slog.Warn("bootstrap: segment delta DECLINED (this client cannot read its own retention floor) and the recovery rebuild was SKIPPED — the auto-heal breaker is latched, so this graph learns no deletions until its rebuild-state record is readable again",
			"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "skipped-breaker-latched")
		return
	}
	out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), g.gt, g.name, true)
	if err != nil {
		slog.Warn("bootstrap: segment delta DECLINED (this client cannot read its own retention floor) and the recovery rebuild FAILED — repair the unreadable rebuild-state record under the segments cache directory (rebuildstate), which is what the next pass reads",
			"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "failed", "error", err)
		return
	}
	slog.Warn("bootstrap: segment delta DECLINED (this client cannot read its own retention floor) — recovered with a from-scratch rebuild, which writes a fresh rebuild-state record for the next pass to read",
		"graph_type", g.gt, "name", g.name, "client_position", since, "rebuild", "attempted",
		"ran", out.Ran, "scanned", out.Scanned, "built", out.Built,
		"partial", out.Partial, "published", out.Published)
	if out.Ran {
		c.armBM25HealProgress(g.gt, g.name)
	}
	c.classifyHealOutcome(g.gt, g.name, out.Ran, out.Scanned)
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
	c.classifyHealOutcome(gt, name, out.Ran, out.Scanned)
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
