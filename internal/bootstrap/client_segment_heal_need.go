// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// healNeedsRebuild is the embed-drain auto-heal's decision: whether (gt, name) needs
// a from-scratch RebuildSegments. It consults BOTH format arms — HNSW first via
// healNeedsRebuildLocal, and when that arm is satisfied, the BM25 arm via
// healNeedsRebuildBM25.
//
// THE MANIFEST-SNAPSHOT ARM IS GONE, THE PER-FORMAT STRUCTURE IS NOT. The cloud rail
// deletion removed the server-manifest branch this used to take first, and both of
// that branch's hand-offs to the BM25 gate went with it — leaving the BM25 arm with
// no production caller at all. It is RE-ROUTED here rather than deleted, for three
// reasons that are on the record rather than aesthetic:
//
//   - THE INCIDENT THIS ARM EXISTS FOR. On 2026-07-27 a collapsed BM25 arm sat
//     behind a perfectly healthy HNSW arm and produced 693 publish skips in 55
//     minutes, curable only by a manual rebuild. The diagnosis was precisely that
//     the degeneracy probe "probes only HNSW". Deleting the BM25 arm because the
//     cloud branch that happened to call it is gone would re-create the defect the
//     incident named.
//   - HEALING TO EXACTLY-CORRECT COUNTS IS A STANDING DIRECTIVE, and a per-graph
//     verdict that never asks the field corpus cannot deliver it.
//   - IT MAKES BM25 AUTO-HEAL REAL FOR THE FIRST TIME. The arm was previously
//     reachable only on the logged-in cloud path; on the local path — now the only
//     path — nothing ever consulted it. Re-routing is not preserving a capability,
//     it is finishing one.
//
// ORDER: HNSW first, and it SHORT-CIRCUITS. A graph whose vector corpus needs a
// from-scratch rebuild gets one; the rebuild covers both formats, so asking the BM25
// arm afterwards would only pay for a second probe to reach the same answer. An
// error from either arm returns without a rebuild — an arm that could not be
// measured must never drive one.
func (c *client) healNeedsRebuild(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	// EVICTED DECLINES FIRST, ahead of the denominator read, so the decline costs
	// ZERO RPC — the same ordering healNeedsRebuildLocal has always used, hoisted
	// here because this function now performs the read that both arms share.
	if c.PoolEvicted(gt, name) {
		return false, nil
	}
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	return c.healNeedsRebuildWith(ctx, gt, name, nil, embedded)
}

// healNeedsRebuildWith is healNeedsRebuild for a caller that ALREADY HOLDS the two
// reads the decision needs: the per-format observations and the graph's
// embedded-node count.
//
// IT EXISTS BECAUSE THE DENOMINATOR IS PER-GRAPH AND THE NUMERATORS ARE PER-FORMAT,
// which is exactly the pattern degenerateAgainstEmbedded's doc states: a caller reads
// the embedded count ONCE and applies it to every arm. Without this entry point the
// reconcile path read it three times per graph per tick — once for its own verdict,
// once inside the HNSW arm and once inside the BM25 arm — and probed the observation
// set twice, which is the same fan-out the per-format collapse was supposed to avoid.
//
// obs may be nil, in which case the BM25 arm probes for itself. embedded is always
// the caller's; there is no sentinel, because a caller reaching this function has by
// definition already paid for it.
func (c *client) healNeedsRebuildWith(
	ctx context.Context, gt kgtypes.GraphType, name string,
	obs []segmentdist.ArmObservation, embedded int,
) (bool, error) {
	needsHNSW, err := c.healNeedsRebuildLocalWith(ctx, gt, name, embedded)
	if err != nil {
		return false, err
	}
	if needsHNSW {
		return true, nil
	}
	// The HNSW arm is satisfied — the field corpus decides, against the SAME
	// denominator.
	return c.healNeedsRebuildBM25With(ctx, gt, name, obs, embedded)
}

// healNeedsRebuildLocal is THE heal decision for the HNSW arm. It computes need from
// LOCAL operands ONLY — a resident doc count and the embedded-node count — with NO
// server presence probe and NO server re-import. There is no server segment store, so
// the presence signal is resident==0 (an empty/lost L2), not a server List.
//
// === WHAT THIS ARM DETECTS, PER ARM, AFTER THE BAND FLIP ===
//
// THE RATIO BAND IS RETIRED FOR THIS ARM AND REPLACED OUTRIGHT by the exact balance
// verdict (balancedAtQuiescence, client_segment_balance.go). The band was never an
// accepted trade-off: a percentage band was rejected as the health definition outright
// — "the % based is just allows for clogs and issues that never heal" — so its three
// blind spots were a BLOCKED DEPENDENCY the whole time, and this is the successor
// landing rather than an improvement being hoped for.
//
// HNSW ARM — WHAT IS DETECTED NOW:
//
//   - AT PIPELINE QUIESCENCE the verdict is EXACT, with ZERO tolerance in both
//     directions: resident is compared against the graph's vector count, and any
//     difference is real. The three blind spots the band admitted are closed here. A
//     graph missing 4,999 of 10,000 documents is a deficit of 4,999; a 63-node graph
//     holding 1 document is a deficit of 62; and a resident count ABOVE the vector
//     count — which the band could not represent at all, both its arms being deficit
//     tests — is now a reportable inequality of its own.
//   - AWAY FROM QUIESCENCE only the EMPTY-POOL trigger runs (hnswPoolLost below).
//     Nothing else is asserted, because nothing else can be: work in flight means the
//     corpus is legitimately mid-convergence, and a numeric band over a moving corpus
//     is exactly what produced the false-unhealthy this ticket removes. That is a
//     REFUSAL TO ASSERT, not a degraded lane — an unmeasurable arm declines to drive a
//     rebuild rather than guessing, exactly as it did before.
//
// MARGIN IS TEMPORAL, NEVER NUMERIC. There is no slack band anywhere on this arm. The
// tolerance is the requirement that the pipeline be QUIESCENT before the verdict is
// formed at all — persistence across the drain edge, not a percentage.
//
// BM25 ARM — UNCHANGED, AND STILL BANDED. client_segment_bm25_gate.go keeps
// degenerateAgainstEmbedded with all three of its blind spots open, and that is
// deliberate rather than an oversight: no exact BM25 corpus count exists yet
// (store.BM25Fields returns nil for a node with no indexable field, so the denominator
// is undefined until the corpus feed defines it), and deleting the band with no exact
// replacement would remove the only detector for the collapse shape that once produced
// 693 publish skips in 55 minutes behind a perfectly healthy HNSW arm.
//
// AND NO MIRROR CROSS-CHECK WAS ADDED TO COMPENSATE. The obvious one — comparing a
// locally-written record against the L2 cache — would compare the cache against
// itself, since the cache is what the record describes. A second reading of one
// authority is not a second authority.
//
// ONE ELABORATION THAT BELONGS TO THIS CALLER, on the empty-pool trigger's flapping:
// a landed rebuild raises resident above 0 and the trigger self-clears on the spot,
// because there is one engine per format and the reset swaps its layer into the very
// engine this read observes. A rebuild that changes nothing leaves resident where it
// was and the trigger correctly re-fires — bounded because RebuildSegments over an
// unchanged corpus is a content-hash no-op.
//
// The manage(status) coverage column is L2-sourced the same way:
// Manager.ShippedSegmentDocCount reads the L2 resident HNSW doc count
// (LoadResidentDocCount), so the whole segment surface (heal AND status) is
// server-free.
func (c *client) healNeedsRebuildLocal(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	// 0. AN EVICTED POOL IS DECLINED FIRST, ahead of BOTH operands, so the decline
	// costs zero RPC and — the point — zero LOAD. LoadResidentDocCount below
	// deliberately MATERIALIZES an evicted pool (it is consumer-side; its OSS branch
	// also feeds the search-visible unified-search verdict), so a background heal tick
	// reaching it would re-materialize the whole pool on every pass just to ask
	// whether it needs a rebuild — the background arms' prohibition violated at the
	// decider rather than at the probe. Declining here is what makes that probe
	// unreachable for an evicted pool.
	if c.PoolEvicted(gt, name) {
		return false, nil
	}
	// GraphEmbeddedCount is the node-graph denominator, and it is the ONLY denominator
	// left: the manifest-derived one its deleted sibling used went with the rail. On
	// error return (false, err) — an arm that could not be measured must never drive a
	// rebuild.
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	return c.healNeedsRebuildLocalWith(ctx, gt, name, embedded)
}

// healNeedsRebuildLocalWith is healNeedsRebuildLocal for a caller that already read
// the embedded-node denominator. It re-checks PoolEvicted rather than trusting the
// caller to have done so: the decline is a pure local read, and an evicted pool
// reaching LoadResidentDocCount below would be MATERIALIZED by it — the background
// arms' prohibition violated at the decider rather than at the probe.
func (c *client) healNeedsRebuildLocalWith(
	ctx context.Context, gt kgtypes.GraphType, name string, embedded int,
) (bool, error) {
	if c.PoolEvicted(gt, name) {
		return false, nil
	}
	// The L2 resident numerator (load-first, L2-only). A load failure is best-effort:
	// WARN and do NOT rebuild — never storm a rebuild on a transient failure.
	resident, err := c.segmentMgr.LoadResidentDocCount(ctx, gt, name)
	if err != nil {
		slog.Warn("bootstrap: auto-heal L2 resident-count load failed; keeping existing resident (no rebuild)",
			"graph_type", gt, "name", name, "err", err)
		return false, nil
	}
	// THE RATIO IS GONE FROM THIS ARM. Away from quiescence the only thing this arm
	// asserts is a LOST POOL — see hnswPoolLost and the per-arm doc block above for why
	// the band's other two branches went with it rather than being re-tuned. The exact
	// verdict runs at the quiescence edge, where the corpus is not moving underneath it.
	return hnswPoolLost(resident, embedded), nil
}

// hnswPoolLost is the HNSW arm's AWAY-FROM-QUIESCENCE trigger, and the only branch of
// the retired ratio band that survives on this arm.
//
// IT IS EXACT, WHICH IS WHY IT SURVIVED. resident 0 with vectors present is a lost cache
// under ANY denominator — there is no ratio in it and no floor below which it stops
// being true — so it is not a band and never was. The two branches that went with the
// band were the RATIO PROBE itself and the sub-floor no-flap guard, and the guard went
// because it existed ONLY to keep the ratio from flapping on a small graph: with no
// ratio left to guard, keeping it would have suppressed the empty-pool trigger on
// exactly the small graphs it was written to protect.
//
// THE FLAPPING ARGUMENT IS UNCHANGED. A landed rebuild raises resident above 0 and the
// trigger self-clears on the spot, because there is one engine per format and the reset
// swaps its layer into the very engine this read observes. A rebuild that changes
// nothing leaves resident where it was and the trigger correctly re-fires — bounded,
// because a rebuild over an unchanged corpus is a content-hash no-op, and bounded again
// by the heal breaker.
//
// AN EVICTED POOL MUST NOT REACH THIS FUNCTION. Its resident count reads 0, which this
// turns into a rebuild — undoing the eviction at the highest possible cost. Every caller
// declines evicted first: healNeedsRebuildLocal via PoolEvicted, the per-format
// consumers via ArmObservation.Evicted.
func hnswPoolLost(resident, embedded int) bool {
	return resident == 0 && embedded > 0
}

// degenerateAgainstEmbedded IS the degeneracy predicate, in one place, for every
// caller that has a resident count and the graph's embedded-node count.
//
// IT IS AN EXTRACTION, NOT A NEW RULE. These three steps in this order are exactly
// what healNeedsRebuildLocal has always applied; they were lifted out unchanged so a
// second consumer could reuse them. That reuse is the point: the per-format
// degeneracy verdict moved out of segmentdist (which lost its shipped
// denominator) into this layer, which still holds the embedded one — and a COPY of
// this predicate beside the original is precisely how the two would drift into
// disagreeing about which graphs need rebuilding.
//
// THE NUMERATOR IS PER-FORMAT AND THE DENOMINATOR PER-GRAPH. A caller reads
// tools.GraphEmbeddedCount ONCE and applies this to each format arm's resident count,
// so the arms stay separately answerable — client_segment_bm25_gate.go exists to ask
// the BM25 arm specifically, and collapsing the formats to one verdict would blind it.
//
//  1. ONE-SHOT EMPTY-POOL TRIGGER, BEFORE the floor gate: an empty/lost pool with
//     embedded nodes present rebuilds REGARDLESS of magnitude. It is the floor-LESS
//     zero-presence heal, and putting it first is what keeps a sub-floor graph with a
//     lost cache from being permanently unsearchable.
//  2. TINY-GRAPH RATIO NO-FLAP: below the floor the ratio is too noisy to consult,
//     and a non-empty pool has already cleared step 1, so a small healthy graph never
//     churns.
//  3. RATIO PROBE: resident covering below tools.CoverageRatioThreshold of the
//     embedded corpus is degenerate.
//
// AN EVICTED POOL MUST NOT REACH THIS FUNCTION. Its resident count reads 0, which
// step 1 turns into a rebuild — undoing the eviction at the highest possible cost.
// Every caller declines evicted first: healNeedsRebuildLocal via PoolEvicted, the
// per-format consumers via ArmObservation.Evicted.
func degenerateAgainstEmbedded(resident, embedded int) bool {
	if resident == 0 && embedded > 0 {
		return true
	}
	if embedded < tools.SegmentCoverageFloor {
		return false
	}
	return float64(resident) < tools.CoverageRatioThreshold*float64(embedded)
}
