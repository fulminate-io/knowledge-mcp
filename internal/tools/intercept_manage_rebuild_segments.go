// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_segments.go — client-side manage(rebuild_segments)
// intercept. rebuild_segments BACKFILLS a code repo's BM25+HNSW search segments
// from nodes that are ALREADY embedded but have ZERO shipped segments (embedded
// before the segment-ship path existed, or after a SegmentStore prune) — WITHOUT
// re-embedding. Unlike rebuild_cache (which lowers to one server-side Index RPC),
// the WORK is CLIENT-driven: the server is engine-free, so the client pages the
// new segment_rebuild PipelineScan axis (already-embedded nodes WITH their stored
// vector + server-composed BM25 fields), rebuilds the segments DETERMINISTICALLY,
// and ships them to the server SegmentStore.
//
// Determinism + idempotency: the build uses the deterministic HNSW path (fixed
// seed + serial-within-segment + concurrent-across-segments), so a re-run over an
// unchanged node set produces byte-identical segments → identical content hash →
// the ship diff is empty → a true no-op. The FIRST rebuild over an embed-segmented
// graph ships the deterministic segments and reconcilePrune drops the superseded
// embed ones server-side; the superseded local .seg files are then evicted via
// InvalidateLocal so they do not orphan under an unbounded cache.
//
// The actual scan/build/ship work — and the per-graphKey single-flight guard —
// live in the reusable RebuildSegments core, which is ALSO invoked by the
// auto-heal closure (built in bootstrap over the segment manager). Both
// callers coalesce onto one run via the shared guard; handleClientRebuildSegments
// is now a thin validate-invoke-render wrapper over that core.

package tools

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuildSegmentsScanPage caps how many segment_rebuild items the driver pulls
// per PipelineScan RPC. It is a wire-batching knob ONLY — independent of the
// segment seal threshold (DefaultMinSegmentDocs); the driver re-chunks the
// accumulated id-ascending stream into exactly-MinSegmentDocs segments itself.
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

// RebuildSegments is the REUSABLE driver core both the manual manage(rebuild_segments)
// op (handleClientRebuildSegments) and the auto-heal closure call. It owns
// the per-graphKey single-flight guard INTERNALLY so the two callers coalesce onto
// one run, and reports WHY it returned via the leading `ran` bool:
//   - ran=false, err==nil  → another rebuild for code/name was already in flight
//     (the caller should treat it as a benign coalesce, NOT an error).
//   - ran=false, err!=nil  → the deps were not wired (nil scanner/shipper) — a
//     genuine misconfiguration the caller surfaces.
//   - ran=true             → a real run completed; scanned/built/pruned carry the
//     counts (scanned==0 means a real run that found nothing to do).
//
// Flow (unchanged from the manual op): page the segment_rebuild scan id-ascending,
// chunk into exactly-MinSegmentDocs groups, build each FULL chunk's HNSW+BM25
// Documents and Add them CONCURRENTLY (NumCPU pool) via the Add-ONLY entrypoints,
// then FlushDeterministic ONCE (seals tail, ships, reconciles) and InvalidateLocal
// the superseded local .seg files.
func RebuildSegments(
	ctx context.Context, scanner PipelineScanner, shipper SegmentShipper, name string,
) (ran bool, scanned, built int, pruned []searchengine.SegmentID, err error) {
	if scanner == nil || shipper == nil {
		return false, 0, 0, nil, fmt.Errorf("rebuild_segments: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// Single-flight per repo: claim or bail with ran=false (no error → coalesce).
	key := "code/" + name
	rebuildSegmentsInFlightMu.Lock()
	if _, busy := rebuildSegmentsInFlight[key]; busy {
		rebuildSegmentsInFlightMu.Unlock()
		return false, 0, 0, nil, nil
	}
	rebuildSegmentsInFlight[key] = struct{}{}
	rebuildSegmentsInFlightMu.Unlock()
	defer func() {
		rebuildSegmentsInFlightMu.Lock()
		delete(rebuildSegmentsInFlight, key)
		rebuildSegmentsInFlightMu.Unlock()
	}()

	items, err := scanRebuildSegments(ctx, scanner, name)
	if err != nil {
		return false, 0, 0, nil, fmt.Errorf("scan failed: %w", err)
	}
	if len(items) == 0 {
		// A real run that found nothing to do: ran=true, zero counts.
		return true, 0, 0, nil, nil
	}

	built, err = buildAndAddRebuildSegments(ctx, shipper, name, items)
	if err != nil {
		return false, 0, 0, nil, fmt.Errorf("build failed (no segments shipped — re-run to retry): %w", err)
	}

	// FINALIZE: the ONE serial ship+reconcile. Seals the deterministic HNSW tail +
	// the BM25 tail, ships once over the fully-published set, returns the pruned ids.
	pruned, err = shipper.FlushDeterministic(ctx, kgtypes.GraphCode, name)
	if err != nil {
		return false, 0, 0, nil, fmt.Errorf("flush/ship failed: %w", err)
	}
	// Evict the superseded embed .seg files locally (T3a return path).
	shipper.InvalidateLocal(kgtypes.GraphCode, name, pruned)

	return true, len(items), built, pruned, nil
}

// handleClientRebuildSegments drives the client-side segment backfill for one
// code repo. It runs SYNCHRONOUSLY (rendering the shipped/ pruned counts) but is
// single-flight per repo. Flow:
//  1. validate graph=="code" + non-empty name; resolve the PipelineScanner +
//     SegmentShipper deps (error "pipeline not wired" on nil).
//  2. page the segment_rebuild scan by the stable after_id id-cursor; terminate
//     on an EMPTY page (the set is stable, so a full final page is normal).
//  3. accumulate items id-ascending; chunk into EXACTLY-DefaultMinSegmentDocs
//     groups; build each FULL chunk's HNSW + BM25 Documents and Add them
//     CONCURRENTLY (NumCPU pool) via the Add-ONLY AddDeterministic / AddFields —
//     NO per-chunk ship (the concurrent-ship/reconcilePrune race fix).
//  4. if any concurrent Add errored, ABORT before flush (no partial ship).
//  5. otherwise FlushDeterministic ONCE (seals the tail, ships, reconciles,
//     returns the pruned ids), then InvalidateLocal evicts the superseded local
//     .seg files.
func handleClientRebuildSegments(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	if a.Graph != "code" {
		return errorResult(`manage(rebuild_segments) requires graph="code" — segments are code-only`)
	}
	if a.Name == "" {
		return errorResult(`manage(rebuild_segments) requires "name" — the repo whose segments to rebuild`)
	}

	scanner := deps.PipelineScanner()
	shipper := deps.SegmentShipper()
	if scanner == nil || shipper == nil {
		return errorResult("manage(rebuild_segments): pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// The single-flight guard + scan/build/flush all live in the shared core
	// (also called by the auto-heal closure). The wrapper only validates,
	// invokes, and renders.
	ran, scanned, built, pruned, err := RebuildSegments(ctx, scanner, shipper, a.Name)
	if err != nil {
		return errorResult("manage(rebuild_segments): " + err.Error())
	}
	if !ran {
		return textResult(fmt.Sprintf("rebuild_segments already in progress for code/%s — ignoring the duplicate request", a.Name))
	}
	if scanned == 0 {
		return textResult(fmt.Sprintf("rebuild_segments: code/%s has no embedded nodes to rebuild segments from — nothing to do", a.Name))
	}

	return textResult(fmt.Sprintf(
		"rebuild_segments complete for code/%s: %d embedded nodes scanned, %d full deterministic chunks built + shipped, %d superseded segments pruned (local cache invalidated). Re-running is a content-hash no-op.",
		a.Name, scanned, built, len(pruned)))
}

// rebuildSegItem is one scanned segment_rebuild node: its id, stored vector, and
// server-composed BM25 fields. Accumulated id-ascending across the paged scan.
type rebuildSegItem struct {
	nodeID     string
	vector     []byte
	bm25Fields map[string]string
}

// scanRebuildSegments pages the segment_rebuild PipelineScan axis by the stable
// after_id id-cursor, accumulating every embedded node (id-ascending). It
// terminates on an EMPTY page — NOT on a short page: the segment_rebuild set is
// stable (a shipped segment never clears a node's vector) so a full final page is
// normal, and only a zero-item page signals exhaustion.
func scanRebuildSegments(ctx context.Context, scanner PipelineScanner, name string) ([]rebuildSegItem, error) {
	var out []rebuildSegItem
	afterID := ""
	for {
		resp, err := scanner.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
			GraphType: string(kgtypes.GraphCode),
			GraphName: name,
			Axis:      "segment_rebuild",
			Limit:     rebuildSegmentsScanPage,
			AfterId:   afterID,
		})
		if err != nil {
			return nil, err
		}
		page := resp.GetItems()
		if len(page) == 0 {
			break // empty page = scan exhausted
		}
		for _, it := range page {
			out = append(out, rebuildSegItem{
				nodeID:     it.GetNodeId(),
				vector:     it.GetBinaryVector(),
				bm25Fields: pipeline.BuildBM25FieldsFromProto(it.GetBm25Fields()),
			})
		}
		// Advance the cursor to the LAST item's id (the scan returns id-ascending).
		afterID = page[len(page)-1].GetNodeId()
	}
	// Defensive: the cursor relies on ascending ids; sort to guarantee the chunk
	// boundaries (and therefore segment membership) are stable even if a backend
	// ever returns an out-of-order page.
	sort.Slice(out, func(i, j int) bool { return out[i].nodeID < out[j].nodeID })
	return out, nil
}

// buildAndAddRebuildSegments chunks the id-ascending items into EXACTLY
// DefaultMinSegmentDocs groups and, for each FULL chunk, builds the HNSW + BM25
// Documents and Adds them CONCURRENTLY (NumCPU pool) via the Add-ONLY
// AddDeterministic / AddFields — no per-chunk ship. The trailing remainder
// (< MinSegmentDocs) is NOT sent via a concurrent Add; FlushDeterministic seals
// that tail. Returns the number of full chunks built. ERROR POLICY: the first
// non-nil Add error is captured and returned; the caller ABORTS before
// FlushDeterministic, so a partial set is never shipped.
func buildAndAddRebuildSegments(ctx context.Context, shipper SegmentShipper, name string, items []rebuildSegItem) (int, error) {
	minDocs := searchengine.DefaultMinSegmentDocs

	// Slice into full exactly-minDocs chunks; the remainder is left for the
	// FlushDeterministic tail seal.
	var chunks [][]rebuildSegItem
	for len(items) >= minDocs {
		chunks = append(chunks, items[:minDocs])
		items = items[minDocs:]
	}
	// The trailing remainder is still Added (Add-only, buffered sub-threshold) so
	// FlushDeterministic can seal it into the final segment.
	tail := items

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, runtime.NumCPU())
		firstErr error
		errOnce  sync.Once
	)
	addChunk := func(chunk []rebuildSegItem) {
		defer wg.Done()
		defer func() { <-sem }()
		hnswDocs, bm25Docs := buildRebuildDocs(chunk)
		if err := shipper.AddDeterministic(ctx, kgtypes.GraphCode, name, hnswDocs); err != nil {
			errOnce.Do(func() { firstErr = err })
			return
		}
		if err := shipper.AddFields(ctx, kgtypes.GraphCode, name, bm25Docs); err != nil {
			errOnce.Do(func() { firstErr = err })
			return
		}
	}

	for _, chunk := range chunks {
		sem <- struct{}{}
		wg.Add(1)
		go addChunk(chunk)
	}
	wg.Wait()
	if firstErr != nil {
		return 0, firstErr
	}

	// Add the sub-threshold tail (buffered, sealed by FlushDeterministic). Done
	// serially after the pool joins so it can't race the concurrent chunks.
	if len(tail) > 0 {
		hnswDocs, bm25Docs := buildRebuildDocs(tail)
		if err := shipper.AddDeterministic(ctx, kgtypes.GraphCode, name, hnswDocs); err != nil {
			return 0, err
		}
		if err := shipper.AddFields(ctx, kgtypes.GraphCode, name, bm25Docs); err != nil {
			return 0, err
		}
	}
	return len(chunks), nil
}

// buildRebuildDocs maps one chunk to its HNSW + BM25 searchengine.Documents via
// the SHARED builders (pipeline.BuildHNSWDocuments / BuildBM25Documents), so the
// rebuild assembles Documents identically to the embed-writeback ship path.
func buildRebuildDocs(chunk []rebuildSegItem) (hnsw, bm25 []searchengine.Document) {
	ids := make([]string, 0, len(chunk))
	vectors := make(map[string][]byte, len(chunk))
	segDocs := make([]pipeline.SegmentDoc, 0, len(chunk))
	for _, it := range chunk {
		ids = append(ids, it.nodeID)
		vectors[it.nodeID] = it.vector
		segDocs = append(segDocs, pipeline.SegmentDoc{NodeID: it.nodeID, Fields: it.bm25Fields})
	}
	return pipeline.BuildHNSWDocuments(vectors, ids), pipeline.BuildBM25Documents(segDocs)
}
