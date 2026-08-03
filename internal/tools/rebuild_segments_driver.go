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
	// Published reports whether the manifest swap LANDED — for BOTH formats, since
	// they carry separate manifests over the same nodes. False with a nil error means
	// the blobs shipped but the live set was never swapped: the corpus is NOT restored.
	Published bool
	// PublishedManifest is how many entries the HNSW manifest holds, READ BACK FROM
	// THE SOURCE after a FULL/RESET rebuild's swap landed. It is -1 when not measured:
	// an incremental run (where the invariant does not apply), a run whose publish did
	// not swap, or a read-back that failed. A non-negative value BELOW Built is the
	// short-manifest condition, and the driver WARNs on it.
	//
	// THE HNSW ARM IS THE ONE READ. The deterministic rebuild publishes the HNSW
	// manifest as its own resident Export — exactly the buckets it built — while the
	// BM25 engine is SHARED with the embed path and legitimately carries extra sealed
	// tails, so comparing that arm to a build count would fail correct work.
	PublishedManifest int
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

	items, tombstoned, servedHorizon, err := scanRebuildSegments(ctx, scanner, gt, name, watermark)
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
		return RebuildOutcome{Ran: true, PublishedManifest: manifestCardinalityUnmeasured}, nil
	}

	// WHICH FINALIZE THIS RUN GETS, and the question is whether the items in hand ARE
	// the corpus. A reset scans from zero by construction, and so does a graph with no
	// persisted watermark; either way this run's buckets are the whole corpus and it is
	// safe to lay one out from scratch. A watermark-scoped run holds a WINDOW, and
	// laying that out from scratch is what appends a thin unaligned segment.
	corpusComplete := reset || watermark == 0

	if !corpusComplete {
		out, applicable, derr := runDeltaRebuild(ctx, shipper, gt, name, items)
		if derr != nil {
			return RebuildOutcome{}, derr
		}
		if applicable {
			return finishRebuild(shipper, gt, name, out, false, watermark, servedHorizon, carried, items)
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
			return RebuildOutcome{Ran: true, PublishedManifest: manifestCardinalityUnmeasured}, nil
		}
	}

	out, err := runCorpusCompleteRebuild(ctx, shipper, gt, name, items)
	if err != nil {
		return RebuildOutcome{}, err
	}
	return finishRebuild(shipper, gt, name, out, reset, watermark, servedHorizon, carried, items)
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
func runCorpusCompleteRebuild(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, items []rebuildSegItem,
) (RebuildOutcome, error) {
	built, partial, err := buildAndAddRebuildSegments(ctx, shipper, gt, name, items)
	if err != nil {
		return RebuildOutcome{}, fmt.Errorf("build failed (no segments shipped — re-run to retry): %w", err)
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
		PublishedManifest: manifestCardinalityUnmeasured,
	}

	// CARDINALITY READ-BACK, CORPUS-COMPLETE RUNS ONLY. A DELTA rebuild legitimately
	// publishes a manifest holding entries this run never built (the partitions it did
	// not touch stay referenced), so this comparison is only meaningful when this run's
	// buckets ARE the whole corpus — which a zero-watermark run is just as much as an
	// explicit reset.
	//
	// IT READS THE MANIFEST BACK FROM THE SOURCE. Comparing `built` against anything
	// derived in this process is an identity — it cannot fail for the reason the check
	// exists. Only what the server actually published can disagree.
	//
	// WHAT IT IS AND IS NOT FOR. The 32-of-128 event was NOT a short manifest: the
	// client's partial L2 cache was the truncation, and that claim is an INFERENCE
	// from a control run rather than a measurement — the live manifest was overwritten
	// before anyone could read it. This check exists because the cardinality was
	// UNVERIFIABLE, not because it was wrong, and it prospectively closes the earlier
	// unexplained 8-blob rebuild that nothing on this path could check at all.
	if res.Swapped {
		out.PublishedManifest = readBackManifestCardinality(ctx, shipper, gt, name, built)
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
func finishRebuild(
	shipper SegmentShipper, gt kgtypes.GraphType, name string,
	out RebuildOutcome, reset bool, watermark, servedHorizon int64,
	carried []searchengine.ExternalID, items []rebuildSegItem,
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
		"published_manifest", out.PublishedManifest)

	if !out.Published {
		slog.Warn("rebuild_segments: publish did not swap the manifest — watermark held (the window is re-scanned next pass)",
			"graph_type", gt, "name", name, "watermark_nanos", watermark, "built", out.Built)
		return out, nil
	}
	// The swap landed, so the partitions this run emitted no longer carry the ids it
	// dropped: retain only the tombstones whose partition was NOT re-emitted.
	if serr := shipper.SaveRebuildState(gt, name, servedHorizon, retainTombstones(carried, items)); serr != nil {
		// The corpus IS published; only the record failed. Report it and keep the old
		// watermark, which costs a re-scan of the same window and loses nothing.
		slog.Warn("rebuild_segments: publish landed but the rebuild state could not be persisted — the window will be re-scanned",
			"graph_type", gt, "name", name, "err", serr)
	}

	return out, nil
}

// manifestCardinalityUnmeasured is RebuildOutcome.PublishedManifest's "no reading"
// value. It is negative rather than zero because ZERO IS A REAL AND ALARMING
// READING — a manifest holding no entries at all — and collapsing "we did not look"
// into it would hide the worst case behind the commonest one.
const manifestCardinalityUnmeasured = -1

// readBackManifestCardinality reads the published HNSW manifest back from the
// source and reports its entry count, WARNing when it holds fewer entries than the
// run reported building.
//
// SHORTER IS THE FAULT; LONGER IS NOT. A manifest with MORE entries than this run
// built is ordinary — the embed path publishes the union of its own resident set
// with the deterministic engine's, so an embed ship landing between the swap and
// this read legitimately grows it. Only a SHORT manifest means content this run
// built is not referenced by the live set.
//
// A FAILED READ-BACK IS NOT A FAILED REBUILD. The corpus is already published at
// this point; refusing the run because a verification read failed would discard
// work that landed. The failure is logged and the cardinality reported unmeasured.
func readBackManifestCardinality(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, built int,
) int {
	published, err := shipper.PublishedManifestCount(ctx, gt, name, hnsw.New().Name())
	if err != nil {
		slog.Debug("rebuild_segments: manifest cardinality read-back unavailable (rebuild unaffected)",
			"graph_type", gt, "name", name, "built", built, "err", err)
		return manifestCardinalityUnmeasured
	}
	if published < built {
		slog.Warn("rebuild_segments: the PUBLISHED manifest holds FEWER entries than this full rebuild built — "+
			"content that was built is not referenced by the live set",
			"graph_type", gt, "name", name, "format", hnsw.New().Name(),
			"built", built, "published_manifest", published)
	}
	return published
}
