// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_segments.go — client-side manage(rebuild_segments)
// intercept. rebuild_segments BACKFILLS a graph's BM25+HNSW search segments
// from nodes that are ALREADY embedded but have ZERO shipped segments (embedded
// before the segment-ship path existed, or after a SegmentStore prune) — WITHOUT
// re-embedding. It serves the builtin code and knowledge graphs AND any
// registered custom graph type (the manual op registry-gates the target); the
// bootstrap auto-heal closure that shares this core is scoped to code + knowledge
// by its own gate. Unlike rebuild_cache (which lowers to one server-side Index RPC),
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
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

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
//   - ran=false, err==nil  → another rebuild for (gt, name) was already in flight
//     (the caller should treat it as a benign coalesce, NOT an error).
//   - ran=false, err!=nil  → the deps were not wired (nil scanner/shipper) — a
//     genuine misconfiguration the caller surfaces.
//   - ran=true             → a real run completed; scanned/built/partial/pruned
//     carry the counts (scanned==0 means a real run that found nothing to do).
//     partial is the length of the sub-threshold tail FlushDeterministic sealed
//     (a graph smaller than one full chunk ships built=0, partial>0 — a real
//     ship, not an empty run).
//
// Flow (unchanged from the manual op): page the segment_rebuild scan id-ascending,
// chunk into exactly-MinSegmentDocs groups, build each FULL chunk's HNSW+BM25
// Documents and Add them CONCURRENTLY (NumCPU pool) via the Add-ONLY entrypoints,
// then FlushDeterministic ONCE (seals tail, ships, reconciles) and InvalidateLocal
// the superseded local .seg files.
func RebuildSegments(
	ctx context.Context, scanner PipelineScanner, shipper SegmentShipper, gt kgtypes.GraphType, name string,
) (ran bool, scanned, built, partial int, pruned []searchengine.SegmentID, err error) {
	if scanner == nil || shipper == nil {
		return false, 0, 0, 0, nil, fmt.Errorf("rebuild_segments: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// Single-flight per (graphType, name): claim or bail with ran=false (no error →
	// coalesce). Keyed on the threaded gt so a custom graph and a code graph of the
	// same name never collide.
	key := string(gt) + "/" + name
	rebuildSegmentsInFlightMu.Lock()
	if _, busy := rebuildSegmentsInFlight[key]; busy {
		rebuildSegmentsInFlightMu.Unlock()
		return false, 0, 0, 0, nil, nil
	}
	rebuildSegmentsInFlight[key] = struct{}{}
	rebuildSegmentsInFlightMu.Unlock()
	defer func() {
		rebuildSegmentsInFlightMu.Lock()
		delete(rebuildSegmentsInFlight, key)
		rebuildSegmentsInFlightMu.Unlock()
	}()

	items, err := scanRebuildSegments(ctx, scanner, gt, name)
	if err != nil {
		return false, 0, 0, 0, nil, fmt.Errorf("scan failed: %w", err)
	}
	if len(items) == 0 {
		// A real run that found nothing to do: ran=true, zero counts.
		return true, 0, 0, 0, nil, nil
	}

	built, partial, err = buildAndAddRebuildSegments(ctx, shipper, gt, name, items)
	if err != nil {
		return false, 0, 0, 0, nil, fmt.Errorf("build failed (no segments shipped — re-run to retry): %w", err)
	}

	// FINALIZE: the ONE serial ship+reconcile. Seals the deterministic HNSW tail +
	// the BM25 tail, ships once over the fully-published set, returns the pruned ids.
	pruned, err = shipper.FlushDeterministic(ctx, gt, name)
	if err != nil {
		return false, 0, 0, 0, nil, fmt.Errorf("flush/ship failed: %w", err)
	}
	// Evict the superseded embed .seg files locally (T3a return path).
	shipper.InvalidateLocal(gt, name, pruned)

	return true, len(items), built, partial, pruned, nil
}

// handleClientRebuildSegments drives the client-side segment backfill for one
// graph (the builtin code or knowledge graph, or a registered custom graph type).
// It runs SYNCHRONOUSLY (rendering the shipped/ pruned counts) but is single-flight
// per (graph,name). Flow:
//  1. registry-gate the graph (builtin code/knowledge OR a registered custom graph
//     type; reject empty / other builtin / unregistered typo; default the knowledge
//     name to "default" and reject a knowledge overlay name) + non-empty name;
//     resolve the PipelineScanner + SegmentShipper deps (error "pipeline not
//     wired" on nil).
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
	// Graph gate, registry-aware (segmentTarget shape): accept the builtin `code`
	// or `knowledge` graph OR any registered custom graph type; reject an empty
	// graph, any other builtin (practice/cloud/cicd carry no rebuildable segments),
	// and an unregistered typo.
	switch {
	case a.Graph == "":
		return errorResult(`manage(rebuild_segments) requires "graph" — name the code, knowledge, or registered custom graph to rebuild`)
	case kgtypes.IsBuiltinGraphType(a.Graph):
		// A builtin carries rebuildable segments iff its graph type is embeddable.
		// Gate on HasRebuildableSegments — the client mirror of the SAME Embeddable()
		// predicate the server's segment_rebuild scan uses — so the client gate and
		// the server scan cannot drift. This admits every embeddable builtin (code,
		// knowledge, cloud, cicd, practice) and still rejects the non-embeddable ones
		// (linkage, transformers, logs, web, pdf), which have no segments to rebuild.
		if !kgtypes.HasRebuildableSegments(kgtypes.GraphType(a.Graph)) {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): builtin graph %q has no rebuildable segments — only embeddable graphs (code, knowledge, cloud, cicd, practice) and registered custom graph types are supported`, a.Graph))
		}
		// The builtin knowledge graph has one canonical instance named "default";
		// an empty name (or the "knowledge" alias) resolves to it. BASE layer only
		// in v1 — an "@"-suffixed overlay/session name is rejected (no overlay
		// segment rebuilds yet).
		if a.Graph == string(kgtypes.GraphKnowledge) {
			if strings.ContainsRune(a.Name, '@') {
				return errorResult(fmt.Sprintf(`manage(rebuild_segments): knowledge overlay name %q is not supported — overlay rebuilds not supported in v1 (base "default" layer only)`, a.Name))
			}
			if a.Name == "" || a.Name == string(kgtypes.GraphKnowledge) {
				a.Name = "default"
			}
		}
	default:
		// Non-builtin: accept only when a GraphTypeDef record resolves (registered
		// custom type), mirroring collect.go:192. A nil registry (degraded client)
		// cannot confirm registration, so the typo error stands.
		crud := deps.GraphTypeCRUD()
		if crud == nil {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): graph %q is not a registered custom graph type (registry unavailable)`, a.Graph))
		}
		if _, found, _ := crud.ByName(ctx, a.Graph); !found {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): graph %q is not code and not a registered custom graph type`, a.Graph))
		}
	}
	if a.Name == "" {
		return errorResult(`manage(rebuild_segments) requires "name" — the repo whose segments to rebuild`)
	}

	// Readiness gate (bind-first startup): rebuild_segments needs the pipeline-backed scanner
	// + shipper, which are nil during the bind-first wiring window. Distinguish the
	// transient window from a permanently degraded (no-pipeline) client so a retry
	// succeeds.
	if !deps.PipelineReady() {
		return errorResult("manage(rebuild_segments): daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	scanner := deps.PipelineScanner()
	shipper := deps.SegmentShipper()
	if scanner == nil || shipper == nil {
		return errorResult("manage(rebuild_segments): pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// The single-flight guard + scan/build/flush all live in the shared core
	// (also called by the auto-heal closure). The wrapper only validates,
	// invokes, and renders.
	ran, scanned, built, partial, pruned, err := RebuildSegments(ctx, scanner, shipper, kgtypes.GraphType(a.Graph), a.Name)
	if err != nil {
		return errorResult("manage(rebuild_segments): " + err.Error())
	}
	if !ran {
		return textResult(fmt.Sprintf("rebuild_segments already in progress for %s/%s — ignoring the duplicate request", a.Graph, a.Name))
	}
	if scanned == 0 {
		return textResult(fmt.Sprintf("rebuild_segments: %s/%s has no embedded nodes to rebuild segments from — nothing to do", a.Graph, a.Name))
	}

	// Manual-op success (scanned>0): re-arm the auto-heal breaker for this graph. An
	// operator asking for a rebuild that actually scanned nodes clears any latched
	// disarm so the automatic embed-drain / reconcile heal resumes — the deliberate
	// manual→clear→auto-refire re-arm (keyed on scanned>0, NOT built>0: built is
	// routinely 0 on a legit sub-1024 heal).
	deps.ClearHealLatch(kgtypes.GraphType(a.Graph), a.Name)

	return textResult(fmt.Sprintf(
		"rebuild_segments complete for %s/%s: %d embedded nodes scanned, %d full deterministic chunks built + shipped, %s, %d superseded segments pruned (local cache invalidated). Re-running is a content-hash no-op.",
		a.Graph, a.Name, scanned, built, renderPartialTail(partial), len(pruned)))
}

// renderPartialTail describes the sub-threshold tail FlushDeterministic sealed,
// so a rebuild over a graph smaller than one full chunk (built=0, partial>0)
// reads as a SUCCESSFUL ship of the tail rather than "0 chunks built". A zero
// tail (the item count is an exact multiple of the chunk size) renders as "no
// partial tail".
func renderPartialTail(partial int) string {
	if partial == 0 {
		return "no partial tail"
	}
	return fmt.Sprintf("%d-node partial tail chunk sealed + shipped", partial)
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
func scanRebuildSegments(ctx context.Context, scanner PipelineScanner, gt kgtypes.GraphType, name string) ([]rebuildSegItem, error) {
	// Re-stamp over the tool-level manage term: the segment-rebuild scan pages
	// the whole vectored corpus, which is worth separating from every other
	// manage op in the metrics.
	ctx = graphclient.WithOperation(ctx, graphclient.OpRebuildSegments)
	var out []rebuildSegItem
	afterID := ""
	for {
		resp, err := scanner.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
			GraphType: string(gt),
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
// that tail. Returns (built, partial): built = the number of FULL chunks built,
// partial = the length of the sub-threshold tail that FlushDeterministic seals
// (0 when the item count is an exact multiple of minDocs). ERROR POLICY: the
// first non-nil Add error is captured and returned; the caller ABORTS before
// FlushDeterministic, so a partial set is never shipped.
func buildAndAddRebuildSegments(ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, items []rebuildSegItem) (built, partial int, err error) {
	minDocs := searchengine.DefaultMinSegmentDocs

	// Slice into full exactly-minDocs chunks; the remainder is left for the
	// FlushDeterministic tail seal.
	var chunks [][]rebuildSegItem
	for len(items) >= minDocs {
		chunks = append(chunks, items[:minDocs])
		items = items[minDocs:]
	}
	// The trailing remainder is still Added (Add-only, buffered sub-threshold) so
	// FlushDeterministic can seal it into the final segment. partial = its length,
	// captured before the Add block so it can be reported even though the tail is
	// only sealed (not full-chunk built).
	tail := items
	partial = len(tail)

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
		if err := shipper.AddDeterministic(ctx, gt, name, hnswDocs); err != nil {
			errOnce.Do(func() { firstErr = err })
			return
		}
		if err := shipper.AddFields(ctx, gt, name, bm25Docs); err != nil {
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
		return 0, 0, firstErr
	}

	// Add the sub-threshold tail (buffered, sealed by FlushDeterministic). Done
	// serially after the pool joins so it can't race the concurrent chunks.
	if len(tail) > 0 {
		hnswDocs, bm25Docs := buildRebuildDocs(tail)
		if aerr := shipper.AddDeterministic(ctx, gt, name, hnswDocs); aerr != nil {
			return 0, 0, aerr
		}
		if aerr := shipper.AddFields(ctx, gt, name, bm25Docs); aerr != nil {
			return 0, 0, aerr
		}
	}
	return len(chunks), partial, nil
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
