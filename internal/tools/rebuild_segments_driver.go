// SPDX-License-Identifier: Apache-2.0

// rebuild_segments_driver.go — the REUSABLE rebuild core both the manual
// manage(rebuild_segments) op and the auto-heal closure call: the per-graphKey
// single-flight guard, the outcome type, the scan/build/finalize sequence, and the
// post-swap manifest cardinality read-back. Relocated verbatim from
// intercept_manage_rebuild_segments.go, which keeps the MCP handler that wraps it.
//
// The handler and the core are separated because they have DIFFERENT CALLERS: the
// core is shared with the bootstrap auto-heal path, which has no tool call, no
// arguments to validate and no text to render. Keeping the core here makes it
// obvious that changing the handler cannot change what the heal path runs.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// rebuildSegmentsScanPage caps how many segment_rebuild items the driver pulls
// per PipelineScan RPC. It is a wire-batching knob ONLY — independent of how the
// driver groups what it scanned; the accumulated stream is regrouped by hash
// bucket once the whole corpus is in hand.
const rebuildSegmentsScanPage = 2048

// rebuildSegmentsInFlight is the per-graphKey single-flight guard. A second
// concurrent rebuild_segments for the SAME repo returns a started-ack rather than
// racing a duplicate scan+ship. Mirrors the server rebuild_cache rebuildInFlight
// shape (engine_index_rebuild_cache.go): a plain Mutex + set, claimed at entry,
// released on completion.
var (
	rebuildSegmentsInFlightMu sync.Mutex
	rebuildSegmentsInFlight   = map[string]struct{}{}
)

// RebuildOutcome is what one rebuild run reports back. SHIPPED AND PUBLISHED ARE
// SEPARATE FACTS and the whole point of the type: Built counts the buckets this run
// emitted and shipped, while Published says whether the manifest swap that makes
// them the live set actually LANDED. A run can ship every bucket and publish
// nothing — the coverage gate refuses a degenerate live set and the agent refuses a
// manifest referencing a blob it has not seen, and BOTH return a nil error — so a
// caller reading Built alone reports success for work that never became durable.
// That is precisely how the failed clean restore was scored as having run.
//
// It is a struct rather than a seventh positional return because Published is the
// value a caller is most likely to drop by accident, and an underscore in a
// six-value destructure is invisible at review.
type RebuildOutcome struct {
	// Ran reports whether a real run happened; see RebuildSegments for the
	// false-with-nil-error coalesce case.
	Ran bool
	// Scanned is the number of embedded nodes the scan returned (tombstones excluded —
	// they are erase instructions, not documents).
	Scanned int
	// Built is the number of hash buckets emitted and shipped. Any non-empty graph,
	// however small, reports at least one.
	Built int
	// Partial is retained at 0 for the callers that log it; hash bucketing has no
	// sub-threshold remainder.
	Partial int
	// HNSWPruned / BM25Pruned are the superseded segment ids each format's publish
	// reaped server-side.
	//
	// THEY ARE SEPARATE BECAUSE THE TWO CORPORA RETIRE INDEPENDENTLY. They carry
	// distinct manifests, so one collapsed count reports whichever format happened to
	// have nothing to say as the whole story — a live run read zero here while all
	// eight bm25 blobs retired, because the vector corpus had already converged and its
	// was the only number reported.
	HNSWPruned []searchengine.SegmentID
	BM25Pruned []searchengine.SegmentID
	// Published reports whether the layer swap LANDED — for BOTH formats, since they
	// index the same nodes independently. False with a nil error means the segments
	// were written but the live set was never swapped: the corpus is NOT restored.
	Published bool
	// ResidentSegmentCount is how many sealed HNSW segments the engine HOLDS after a
	// FULL/RESET rebuild's swap landed. It is -1 when not measured: an incremental run
	// (where the invariant does not apply) or a run whose swap did not land. A
	// non-negative value BELOW Built is the short-set condition, and the driver WARNs
	// on it.
	//
	// IT REPLACED A PUBLISHED-MANIFEST CARDINALITY, read back from the server. The
	// operand changed but the QUESTION did not: is the live set short of what this
	// run's corpus derives? Built is that derivation (BucketCountFor over the scanned
	// corpus), so the pair remains two independent numbers rather than one number
	// compared against itself.
	//
	// THE HNSW ARM IS THE ONE READ. The reset builds the HNSW layer as exactly the
	// buckets it derived, while the BM25 engine is SHARED with the embed path and
	// legitimately carries extra sealed tails, so comparing that arm to a build count
	// would fail correct work.
	ResidentSegmentCount int
	// Degraded is THIS RUN'S per-class census of input the builds dropped — the
	// record is cleared immediately before the finalize and read immediately
	// after, so it is not a running total.
	//
	// THE OTHER SURFACE'S WINDOW IS DIFFERENT and they render side by side, so the
	// difference is stated here: the manage(status) coverage row reports the same
	// durable record and therefore the same number until the next rebuild clears
	// it, PLUS anything the background BM25 arm has dropped since.
	Degraded map[string]int
}

// RebuildSegments is the REUSABLE driver core both the manual manage(rebuild_segments)
// op (handleClientRebuildSegments) and the auto-heal closure call. It owns
// the per-graphKey single-flight guard INTERNALLY so the two callers coalesce onto
// one run, and reports WHY it returned via the outcome's Ran bool:
//   - Ran=false, err==nil  → another rebuild for (gt, name) was already in flight
//     (the caller should treat it as a benign coalesce, NOT an error).
//   - Ran=false, err!=nil  → the deps were not wired (nil scanner/shipper) — a
//     genuine misconfiguration the caller surfaces.
//   - Ran=true             → a real run completed; the counts carry the detail
//     (Scanned==0 means a real run that found nothing to do), and Published says
//     whether it became durable.
//
// Flow: load the persisted watermark, page the segment_rebuild scan id-ascending
// from it, group the accumulated items by hash bucket, build each group's
// HNSW+BM25 Documents CONCURRENTLY (NumCPU pool), Add and SEAL each group serially
// so one bucket becomes one segment, then FinalizeRebuild ONCE (ships,
// reconciles), InvalidateLocal the superseded local .seg files, and — only if that
// finalize LANDED a manifest swap — persist the advanced watermark.
//
// reset scans from zero regardless of what is persisted, and drops the retained
// tombstone ids, so an operator always has a from-scratch escape hatch. It is what
// the manual op's reset flag lowers to, and what the auto-heal path passes: a heal
// runs precisely because the shipped corpus is missing or degenerate, so it needs
// the whole corpus rebuilt, not the slice of it that changed recently.
func RebuildSegments(
	ctx context.Context, scanner PipelineScanner, shipper SegmentShipper, gt kgtypes.GraphType, name string, reset bool,
) (RebuildOutcome, error) {
	if scanner == nil || shipper == nil {
		return RebuildOutcome{}, fmt.Errorf("rebuild_segments: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// Single-flight per (graphType, name): claim or bail with ran=false (no error →
	// coalesce). Keyed on the threaded gt so a custom graph and a code graph of the
	// same name never collide.
	key := string(gt) + "/" + name
	rebuildSegmentsInFlightMu.Lock()
	if _, busy := rebuildSegmentsInFlight[key]; busy {
		rebuildSegmentsInFlightMu.Unlock()
		return RebuildOutcome{}, nil
	}
	rebuildSegmentsInFlight[key] = struct{}{}
	rebuildSegmentsInFlightMu.Unlock()
	defer func() {
		rebuildSegmentsInFlightMu.Lock()
		delete(rebuildSegmentsInFlight, key)
		rebuildSegmentsInFlightMu.Unlock()
	}()

	// The watermark scopes the scan to what changed after the last LANDED rebuild.
	// A zero watermark is the full-corpus scan, byte-for-byte the behavior before
	// this record existed, which is what a fresh daemon, a wiped L2 cache and an
	// operator reset all resolve to.
	//
	// AN UNREADABLE RECORD DEGRADES TO ZERO rather than failing the rebuild. Every
	// error here — a corrupt file, a permissions problem — is resolved correctly by
	// rebuilding the whole corpus once; refusing to rebuild is the only response
	// that leaves the graph unserved.
	var (
		watermark int64
		retained  []searchengine.ExternalID
	)
	if !reset {
		var lerr error
		watermark, retained, lerr = shipper.LoadRebuildState(gt, name)
		if lerr != nil {
			slog.Warn("rebuild_segments: rebuild state unreadable — falling back to a FULL corpus rebuild",
				"graph_type", gt, "name", name, "err", lerr)
			watermark, retained = 0, nil
		}
	}

	// THE VALUE SENT IS THE FLOOR ACROSS THE CONSUMERS THAT HOLD A POSITION, not
	// this drain's own watermark. The server raises its retention watermark from
	// whatever a scan carries, so reporting this consumer's position alone would
	// raise it past an erasure the delta-merge consumer has not read, and the reap
	// behind it would destroy that erasure permanently.
	//
	// ONE FIELD CARRIES BOTH MEANINGS, so the floor is also this scan's lower
	// bound. That is why a peer with NO position is excluded rather than treated as
	// zero: including it would drag this read down to the whole corpus. Where the
	// floor does sit below this drain's own watermark, the drain re-reads rows it
	// has already published — idempotent, and the direction that costs work rather
	// than losing a deletion.
	items, tombstoned, servedHorizon, err := scanRebuildSegments(
		ctx, scanner, gt, name, retentionFloorFor(shipper, gt, name, watermark))
	if err != nil {
		return RebuildOutcome{}, fmt.Errorf("scan failed: %w", err)
	}

	// Hand the engines every id known deleted — the ones this scan reported plus the
	// ones earlier scans reported whose partitions have not been re-emitted since —
	// so an Import of a blob shipped BEFORE a delete seeds it dead instead of
	// bringing the node back.
	// Stamp this scan's own reported deletes before seeding, so no interleaving can
	// observe an id as tombstoned-without-a-stamp. It is the scan's slice and never
	// `carried`: the merged set holds deletes older passes reported, and re-dating
	// those would suppress writes that legitimately followed them.
	shipper.NoteDeletedIDs(gt, name, tombstoned)
	carried := unionTombstones(retained, tombstoned)
	shipper.SetGraphTombstones(gt, name, carried)

	if len(items) == 0 {
		// A real run that found nothing to build: Ran=true, zero counts. The watermark
		// deliberately does NOT advance — no publish ran, so nothing has become
		// durable, and a scan that costs one empty page is not worth a durable write.
		// Published stays false for the same reason: no manifest swap happened.
		// The ids reported this pass are re-reported by the next scan for the same
		// reason.
		return RebuildOutcome{Ran: true, ResidentSegmentCount: derivedBucketCardinalityUnmeasured}, nil
	}

	// WHICH FINALIZE THIS RUN GETS, and the question is whether the items in hand ARE
	// the corpus. A reset scans from zero by construction, and so does a graph with no
	// persisted watermark; either way this run's buckets are the whole corpus and it is
	// safe to lay one out from scratch. A watermark-scoped run holds a WINDOW, and
	// laying that out from scratch is what appends a thin unaligned segment.
	corpusComplete := reset || watermark == 0

	if !corpusComplete {
		out, emittedBucketCount, applicable, derr := runDeltaRebuild(ctx, shipper, gt, name, items)
		if derr != nil {
			return RebuildOutcome{}, derr
		}
		if applicable {
			// The count the re-emit ran at, reported by the delta itself: it is derived
			// from the RESIDENT corpus, not from this window, and the trim is wrong
			// under any other.
			return finishRebuild(
				shipper, gt, name, out, false, watermark, servedHorizon, carried, items, emittedBucketCount)
		}
		// NOT APPLICABLE — and the recovery must RE-SCAN, not reuse what is in hand.
		// These items are the watermark-scoped delta; driving the from-scratch path with
		// them would build a manifest out of the window alone and publish it as the whole
		// live set, making dropped = the entire rest of the corpus. The coverage gate does
		// NOT save it either: the ratio is disarmed on exactly the near-empty resident set
		// that makes a delta inapplicable, so the publish would land, report Swapped, and
		// advance the watermark past the window it just reaped.
		slog.Warn("rebuild_segments: delta path not applicable (serving engines hold no corpus) — falling back to a FULL re-scan from zero",
			"graph_type", gt, "name", name, "watermark_nanos", watermark, "delta_items", len(items))
		items, tombstoned, servedHorizon, err = scanRebuildSegments(ctx, scanner, gt, name, 0)
		if err != nil {
			return RebuildOutcome{}, fmt.Errorf("fallback full re-scan failed: %w", err)
		}
		// The re-scan reported its own window; stamp it before seeding, as above.
		shipper.NoteDeletedIDs(gt, name, tombstoned)
		carried = unionTombstones(retained, tombstoned)
		shipper.SetGraphTombstones(gt, name, carried)
		if len(items) == 0 {
			return RebuildOutcome{Ran: true, ResidentSegmentCount: derivedBucketCardinalityUnmeasured}, nil
		}
		// The re-scan above went from a ZERO watermark, so the items now in hand ARE the
		// corpus even though this run did not start out corpus-complete. Nothing
		// downstream is told that any more — the finalize decides from the resident set
		// it can see — but the fact is what makes the fallback safe, and
		// TestFullReScanPutsAZeroOnTheWire is where it is asserted, on the watermark this
		// re-scan puts on the wire.
	}

	out, err := runCorpusCompleteRebuild(ctx, shipper, gt, name, items)
	if err != nil {
		return RebuildOutcome{}, err
	}
	// count-provenance: corpus-derived — this arm is reached only when corpusComplete
	// holds (or after the fallback re-scan from a zero watermark), so items IS the
	// corpus, and this is the same expression buildAndAddRebuildSegments grouped the
	// partitions under. Computing it at the call site is what makes that agreement
	// visible rather than assumed.
	return finishRebuild(
		shipper, gt, name, out, reset, watermark, servedHorizon, carried, items,
		searchengine.BucketCountFor(len(items)))
}

// runCorpusCompleteRebuild is the FROM-SCRATCH finalize: stage this run's partitions,
// one call per hash bucket, then finalize once — building each format's layer aside,
// shipping it, swapping it in whole and publishing it as the writer's manifest, retiring
// whatever it superseded.
//
// NOTHING OPENS THE RUN, and that absence is the collapse. The staged partitions live in
// a work map the finalize takes ONCE, so this run's layer is its own by construction —
// where the two-engine shape needed a reset step to pin the outgoing layer and drop a
// staging engine before the first write, and published the union of both layers whenever
// a write beat it.
//
// IT TAKES NO CORPUS-COMPLETE CLAIM ANY MORE, and the removal is the point rather than
// an omission. The claim used to travel from the one place that chose the scan's
// watermark down to the finalize, which decided from it whether the built layer outranked
// the PRIOR MANIFEST's summed doc count. There is no prior manifest and no such
// comparison — FinalizeRebuild takes (ctx, gt, name) and nothing else — so the argument
// arrived here and went nowhere. A parameter that is threaded, documented and never read
// is worse than no parameter: every reader spends the same effort on it, and the doc
// keeps asserting a gate that no longer exists.
//
// WHAT REPLACED THE GATE IS LOCAL. The finalize builds each format's layer aside and
// refuses a prospective layer that would retire a populated one in favor of nothing,
// which it decides from the resident set it can see — no second authority, and so no
// claim to be told.
func runCorpusCompleteRebuild(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string,
	items []rebuildSegItem,
) (RebuildOutcome, error) {
	built, partial, stagedBM25, err := buildAndAddRebuildSegments(ctx, shipper, gt, name, items)
	if err != nil {
		return RebuildOutcome{}, fmt.Errorf("build failed (no segments shipped — re-run to retry): %w", err)
	}

	// RESET THE BM25 ARM'S FEED CURSOR BEFORE THE SWAP, whenever this run stages BM25
	// work. The layer about to be swapped in was built from a VECTOR-GATED scan, so it
	// holds nothing for a node that is embed-eligible but not yet embedded — while the
	// arm's cursor is already past those nodes. Their later embed writeback does not
	// move updated_at and the embed axis no longer ships BM25, so without this reset
	// their keyword documents are gone until their TEXT changes, which on a stable
	// corpus is never.
	//
	// BEFORE, NOT AFTER, and every window is safe under that order: crash after the
	// reset and before the swap leaves a zero cursor against the OLD layer (next tick
	// re-drains — redundant, correct); crash after the swap leaves a zero cursor
	// against the NEW layer (next tick re-drains and re-establishes exactly what the
	// swap dropped). The after-ordering has a window where the cursor is ahead of the
	// engine's documents and the loss is permanent.
	//
	// A FAILED RESET ABORTS THE RUN rather than warning and continuing. Swapping the
	// layer with a stale cursor still standing is the precise state this reset exists
	// to prevent, and it is not self-healing: no later pass re-derives the position.
	// Aborting before the finalize leaves the OLD layer serving and the OLD cursor
	// consistent with it, which is a re-runnable state rather than a lossy one.
	//
	// stagedBM25 IS THE TRIGGER, NOT RebuildFinalizeResult.Swapped — see
	// buildAndAddRebuildSegments for why that flag can be true with no layer swap.
	if stagedBM25 {
		if rerr := shipper.ResetBM25Cursors(gt, name); rerr != nil {
			return RebuildOutcome{}, fmt.Errorf(
				"bm25 cursor reset failed before the layer swap (no segments swapped — re-run to retry): %w", rerr)
		}
	}

	// CLEAR THE DROP CENSUS IMMEDIATELY BEFORE THE FINALIZE, and the POSITION IS THE
	// CORRECTNESS PROPERTY: the finalize is where the segment builds happen, because
	// StageRebuildPartition's own contract says it adds nothing to any engine and
	// ships nothing — so no Build, and therefore no drop, can occur between the
	// staging loop and this line. Clearing at the top of the run instead would fold
	// in whatever the background BM25 arm dropped while the scan was paging.
	//
	// A FAILED CLEAR IS NON-FATAL, unlike the cursor reset a few lines above which
	// ABORTS, and the asymmetry is reasoned rather than incidental: a stale cursor
	// loses documents permanently, while a stale census only OVER-REPORTS drops —
	// and over-reporting must not abort a rebuild that would restore the corpus.
	if cerr := shipper.ResetBM25DegradeCounts(gt, name); cerr != nil {
		slog.Warn("rebuild_segments: could not clear the BM25 drop census before the finalize — this run's reported drop count may include earlier drops",
			"graph_type", gt, "name", name, "error", cerr)
	}

	// FINALIZE: the ONE serial build-aside + ship + gate + swap + publish, per format.
	// There is no staging engine to reset first — the partitions were staged in a work
	// map and this call takes them once.
	res, err := shipper.FinalizeRebuild(ctx, gt, name)
	if err != nil {
		return RebuildOutcome{}, fmt.Errorf("finalize failed: %w", err)
	}
	// Evict the superseded HNSW .seg files locally. The BM25 retired set is REPORTED but
	// not evicted here: local BM25 orphans are PruneCache's job, and reporting a set is
	// not the same as evicting it.
	shipper.InvalidateLocal(gt, name, res.HNSWSuperseded)

	out := RebuildOutcome{
		Ran: true, Scanned: len(items), Built: built, Partial: partial,
		HNSWPruned: res.HNSWSuperseded, BM25Pruned: res.BM25Superseded, Published: res.Swapped,
		ResidentSegmentCount: derivedBucketCardinalityUnmeasured,
		Degraded:             shipper.BM25DegradeCounts(gt, name),
	}

	// CARDINALITY READ-BACK, CORPUS-COMPLETE RUNS ONLY. A DELTA rebuild legitimately
	// publishes a manifest holding entries this run never built (the partitions it did
	// not touch stay referenced), so this comparison is only meaningful when this run's
	// buckets ARE the whole corpus — which a zero-watermark run is just as much as an
	// explicit reset.
	//
	// IT COMPARES THE DERIVATION AGAINST THE PRESENT SET. `built` is BucketCountFor
	// over the scanned corpus — how many partitions that corpus should occupy — and
	// the engine reports how many it holds. Comparing `built` against a number the
	// same run reported ABOUT ITSELF would be an identity that cannot fail for the
	// reason the check exists; these two are not that.
	//
	// WHAT IT IS AND IS NOT FOR. The 32-of-128 event was NOT a short manifest: the
	// client's partial L2 cache was the truncation, and that claim is an INFERENCE
	// from a control run rather than a measurement — the live manifest was overwritten
	// before anyone could read it. This check exists because the cardinality was
	// UNVERIFIABLE, not because it was wrong, and it prospectively closes the earlier
	// unexplained 8-blob rebuild that nothing on this path could check at all.
	if res.Swapped {
		out.ResidentSegmentCount = readResidentSegmentCardinality(shipper, gt, name, built)
	}
	return out, nil
}

// finishRebuild is the tail both arms share: the operator-visible completion line and
// the watermark advance.
//
// ADVANCE ONLY ON A COMPLETED SWAP, and only to the horizon the SERVER served.
//
// The nil error is not the completion signal: a publish is skipped, and returns nil,
// whenever the coverage gate rejects a degenerate live set or the agent reports a
// referenced blob it has not seen yet. Advancing on a skip is what makes the hole
// permanent — the window's content was never published, and a watermark past it means
// those rows are never scanned again.
//
// The horizon is SERVER-SERVED for the same class of reason: a client clock can read
// the same instant as the writes it is meant to exclude, and the server's strict
// after-comparison would then drop exactly those rows.
// emittedBucketCount is named for WHICH count it is, because the trim below is only
// correct under the count this run's re-emit actually ran at — each arm supplies its
// own, and neither can be re-derived here from the items in hand.
func finishRebuild(
	shipper SegmentShipper, gt kgtypes.GraphType, name string,
	out RebuildOutcome, reset bool, watermark, servedHorizon int64,
	carried []searchengine.ExternalID, items []rebuildSegItem, emittedBucketCount int,
) (RebuildOutcome, error) {
	// OPERATOR-VISIBLE COMPLETION LINE. The rebuild runs inside the MCP client
	// intercept chain, which emitted exactly three lines for a 78-second run that
	// truncated a served corpus to a quarter — no ship line, no publish line, no WARN.
	// These are the counts this layer OWNS: what it scanned, what it built, whether
	// the swap landed, and the read-back cardinality. The per-format shipped-versus-
	// skipped-as-present counts do NOT appear here on purpose — they do not exist at
	// this layer, and segmentdist emits them where they do.
	slog.Info("rebuild_segments: run complete",
		"graph_type", gt, "name", name, "reset", reset,
		"scanned", out.Scanned, "built", out.Built, "partial", out.Partial,
		"published", out.Published,
		"hnsw_pruned", len(out.HNSWPruned), "bm25_pruned", len(out.BM25Pruned),
		"resident_segments", out.ResidentSegmentCount)

	if !out.Published {
		slog.Warn("rebuild_segments: publish did not swap the manifest — watermark held (the window is re-scanned next pass)",
			"graph_type", gt, "name", name, "watermark_nanos", watermark, "built", out.Built)
		return out, nil
	}
	// The swap landed, so the partitions this run emitted no longer carry the ids it
	// dropped: retain only the tombstones whose partition was NOT re-emitted.
	if serr := shipper.SaveRebuildState(
		gt, name, servedHorizon, retainTombstones(carried, items, emittedBucketCount)); serr != nil {
		// The corpus IS published; only the record failed. Report it and keep the old
		// watermark, which costs a re-scan of the same window and loses nothing.
		slog.Warn("rebuild_segments: publish landed but the rebuild state could not be persisted — the window will be re-scanned",
			"graph_type", gt, "name", name, "err", serr)
	}

	return out, nil
}

// derivedBucketCardinalityUnmeasured is RebuildOutcome.ResidentSegmentCount's "no
// reading" value. It is negative rather than zero because ZERO IS A REAL AND ALARMING
// READING — an engine holding no sealed segments at all — and collapsing "we did not
// look" into it would hide the worst case behind the commonest one.
const derivedBucketCardinalityUnmeasured = -1

// readResidentSegmentCardinality reads how many sealed HNSW segments the engine HOLDS
// after the swap and reports it, WARNing when it holds fewer than the corpus DERIVES.
//
// THE TWO OPERANDS COME FROM DIFFERENT PLACES, which is the whole reason this is a
// gate rather than a formality. `built` is searchengine.BucketCountFor over the
// scanned corpus — a DERIVATION saying how many partitions that corpus should occupy.
// `present` is what the engine actually holds. They diverge when a bucket failed to
// build or a swap landed short.
//
// ITS PREDECESSOR READ A SERVER MANIFEST, and the swap had to be made carefully. That
// version's own doc said comparing `built` against anything derived in-process "is an
// identity — it cannot fail for the reason the check exists". A naive local
// substitution — comparing the build count against a number the same run reported
// about itself — would have created exactly that identity and left a check that reads
// as coverage while being incapable of failing. Derived-versus-present is not that:
// neither side is the other's restatement.
//
// SHORTER IS THE FAULT; LONGER IS NOT. An engine holding MORE segments than the
// derivation predicts is ordinary — an embed drain sealing a tail between the swap
// and this read legitimately grows it. Only a SHORT set means content this run built
// is not in the live set.
//
// IT CANNOT FAIL, so unlike its predecessor it has no unmeasured path of its own: the
// read is one atomic snapshot load. The unmeasured sentinel survives for the caller's
// "did not look" case only.
func readResidentSegmentCardinality(
	shipper SegmentShipper, gt kgtypes.GraphType, name string, built int,
) int {
	present := shipper.ResidentSegmentCount(gt, name, hnsw.New().Name())
	if present < built {
		slog.Warn("rebuild_segments: the engine holds FEWER sealed segments than this full rebuild's corpus derives — "+
			"content that was built is not in the live set",
			"graph_type", gt, "name", name, "format", hnsw.New().Name(),
			"derived", built, "resident_segments", present)
	}
	return present
}
