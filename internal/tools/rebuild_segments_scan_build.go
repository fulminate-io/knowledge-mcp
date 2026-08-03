// SPDX-License-Identifier: Apache-2.0

// rebuild_segments_scan_build.go — the rebuild driver's SCAN and BUILD legs: the
// scanned-item carrier, the paged segment_rebuild scan (with its change-scoping
// watermark and tombstone split), and the hash-bucket grouping that Adds and SEALS
// one segment per bucket. Relocated verbatim from intercept_manage_rebuild_segments.go,
// which keeps the MCP handler.
//
// Scan and build travel together because the second consumes exactly what the first
// produces, and because the ORDER guarantee is shared: the scan sorts id-ascending
// and the build relies on that order for the byte-identical re-run property. Reading
// one without the other loses the reason either sorts at all.

package tools

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

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
//
// afterStampedAtNanos is the CHANGE-SCOPING watermark. ZERO — what every caller
// passes today — asks for the full vectored corpus, exactly as before. A non-zero
// value asks the server for only what changed after it, which is what makes a
// rebuild cost work proportional to the change rather than to the corpus.
//
// The page is split by the per-item tombstone flag. A tombstoned item is an ERASE
// instruction and carries NO payload (no vector, no BM25 fields) — feeding one to
// the document builders would add a zero-vector document to the HNSW segment, so
// the split is a correctness guard, not bookkeeping. Its id goes to the returned
// tombstoned list instead.
//
// servedHorizonNanos is the safe horizon the server served this scan up to. A
// caller advances its stored watermark to THIS value — and only after the publish
// that consumed the page succeeds. It is deliberately server-supplied: a watermark
// read from the local clock can land in the same instant as the writes it is meant
// to exclude, and the server's strict after-comparison would then drop exactly
// those rows.
func scanRebuildSegments(
	ctx context.Context, scanner PipelineScanner, gt kgtypes.GraphType, name string, afterStampedAtNanos int64,
) (items []rebuildSegItem, tombstoned []string, servedHorizonNanos int64, err error) {
	// Re-stamp over the tool-level manage term: the segment-rebuild scan pages
	// the whole vectored corpus, which is worth separating from every other
	// manage op in the metrics.
	return scanRebuildSegmentsAs(ctx, graphclient.OpRebuildSegments, scanner, gt, name, afterStampedAtNanos)
}

// scanRebuildSegmentsAs is scanRebuildSegments' body with the operation term the
// caller's choice. The term is a parameter rather than a constant because TWO
// callers page this axis with genuinely different load shapes — the on-demand
// full-corpus rebuild and the per-tick bounded tombstone delta — and collapsing
// them into one bucket would hide a delta read that had degenerated into a full
// scan inside the rebuild's traffic.
func scanRebuildSegmentsAs(
	ctx context.Context, op graphclient.Operation,
	scanner PipelineScanner, gt kgtypes.GraphType, name string, afterStampedAtNanos int64,
) (items []rebuildSegItem, tombstoned []string, servedHorizonNanos int64, err error) {
	ctx = graphclient.WithOperation(ctx, op)
	var out []rebuildSegItem
	afterID := ""
	for {
		resp, err := scanner.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
			GraphType:           string(gt),
			GraphName:           name,
			Axis:                "segment_rebuild",
			Limit:               rebuildSegmentsScanPage,
			AfterId:             afterID,
			AfterStampedAtNanos: afterStampedAtNanos,
		})
		if err != nil {
			return nil, nil, 0, err
		}
		// The horizon is echoed on every page; the LAST one observed bounds the whole
		// drain, since each page is served on its own snapshot at or after the prior.
		servedHorizonNanos = resp.GetServedHorizonNanos()
		page := resp.GetItems()
		if len(page) == 0 {
			break // empty page = scan exhausted
		}
		for _, it := range page {
			if it.GetTombstoned() {
				tombstoned = append(tombstoned, it.GetNodeId())
				continue
			}
			out = append(out, rebuildSegItem{
				nodeID:     it.GetNodeId(),
				vector:     it.GetBinaryVector(),
				bm25Fields: pipeline.BuildBM25FieldsFromProto(it.GetBm25Fields()),
			})
		}
		// Advance the cursor to the LAST item's id (the scan returns id-ascending).
		// Taken from the raw page, NOT from out — a page whose tail is a tombstone
		// still has to advance the cursor past it, or the drain re-reads it forever.
		afterID = page[len(page)-1].GetNodeId()
	}
	// Defensive: the cursor relies on ascending ids; sort to guarantee the chunk
	// boundaries (and therefore segment membership) are stable even if a backend
	// ever returns an out-of-order page.
	sort.Slice(out, func(i, j int) bool { return out[i].nodeID < out[j].nodeID })
	return out, tombstoned, servedHorizonNanos, nil
}

// ReadServedHorizon asks the server for ONE segment_rebuild row solely to read the
// safe horizon the scan echoes on it.
//
// The CLIENT-SIDE contract is the one that matters here and it is flavor-neutral:
// every page of a segment_rebuild scan carries the horizon the server served it,
// and the last page observed bounds the whole drain (see scanRebuildSegmentsAs). A
// page carrying no rows still carries a horizon, and a ZERO means only "this scan
// was served no horizon" — never a claim about which backend answered or why. So a
// caller persists the value only when it is above zero.
//
// Limit 1 rather than 0: a zero limit means "no LIMIT clause" to the server's query
// builder, which is the whole-corpus read this probe exists to avoid.
func ReadServedHorizon(
	ctx context.Context, scanner PipelineScanner, gt kgtypes.GraphType, name string,
) (int64, error) {
	if scanner == nil {
		return 0, fmt.Errorf("segment horizon seed: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}
	ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentHorizonSeed)
	resp, err := scanner.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
		GraphType: string(gt),
		GraphName: name,
		Axis:      "segment_rebuild",
		Limit:     1,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetServedHorizonNanos(), nil
}

// buildAndAddRebuildSegments groups the scanned items by HASH BUCKET so the finalize
// emits one segment per bucket: it builds each group's HNSW + BM25 Documents
// CONCURRENTLY (NumCPU pool — the expensive part), then STAGES each group serially in
// ascending bucket order through StageRebuildPartition — no per-bucket ship, and no
// write to any engine.
//
// Membership is a function of the NODE, not of its position in the scan, so
// inserting one node rewrites only the bucket that receives it instead of
// re-cutting every group after it.
//
// THE STAGING PHASE IS SERIAL ON PURPOSE. The staged partitions are an ORDERED list and
// the finalize builds one segment per entry, so the order they are appended in is the
// layout it produces. Only the Document building runs in the pool.
//
// A group is staged as its own partition rather than merged with its neighbours because
// two buckets sharing a segment is exactly the membership mixing this grouping removes.
// Under the retired Add+Seal shape that separation had to be enforced by force-sealing
// each group; staging gives it by construction.
//
// Returns (built, partial): built = the number of buckets emitted; partial is
// always 0, since every bucket is emitted as its own segment regardless of size
// and there is no sub-threshold remainder concept. ERROR POLICY unchanged: the
// first non-nil error is returned and the caller ABORTS before FinalizeRebuild,
// so an incomplete set is never shipped.
func buildAndAddRebuildSegments(ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, items []rebuildSegItem) (built, partial int, err error) {
	// One bucket per ~DefaultMinSegmentDocs documents, derived from the corpus size
	// and stable under small drift.
	bucketCount := searchengine.BucketCountFor(len(items))
	groups := make(map[int][]rebuildSegItem, bucketCount)
	for _, it := range items {
		b := searchengine.BucketOf(it.nodeID, bucketCount)
		groups[b] = append(groups[b], it)
	}
	// Ascending bucket order so emission order is deterministic. Within a bucket the
	// members keep the scan's id-ascending order, which is what the deterministic
	// builder needs for the byte-identical re-run property.
	buckets := make([]int, 0, len(groups))
	for b := range groups {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)

	// Build every group's Documents concurrently, indexed by emission order.
	docs := make([]struct{ hnsw, bm25 []searchengine.Document }, len(buckets))
	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, runtime.NumCPU())
	)
	for i, b := range buckets {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, group []rebuildSegItem) {
			defer wg.Done()
			defer func() { <-sem }()
			docs[i].hnsw, docs[i].bm25 = buildRebuildDocs(group)
		}(i, groups[b])
	}
	wg.Wait()

	// STAGE serially, in ascending bucket order. One call carries BOTH formats' share
	// of the partition, so the two corpora cannot diverge by a caller staging one and
	// forgetting the other. Nothing is added to an engine and nothing ships: the
	// finalize builds each layer aside and swaps it in whole.
	for i := range docs {
		if serr := shipper.StageRebuildPartition(ctx, gt, name, docs[i].hnsw, docs[i].bm25); serr != nil {
			return 0, 0, serr
		}
	}
	return len(buckets), 0, nil
}

// buildRebuildDeltaDocs is buildAndAddRebuildSegments' sibling for the DELTA path: it
// maps the whole scanned window to its HNSW + BM25 Documents and hands them over
// UNGROUPED, adding nothing to any engine.
//
// NO HASH-BUCKET GROUPING, and that is the whole difference. The full path groups
// because it is laying out a corpus from scratch and each group must become its own
// segment. A delta is not laying anything out: the partition machinery on the other
// side of the seam derives the count from the corpus already resident and groups these
// documents itself, so grouping them here would impose a count derived from the
// WINDOW — one partition for a small window — and collapse whatever it touched into it.
//
// It reuses buildRebuildDocs verbatim so both paths assemble Documents through the
// same shared pipeline builders, and therefore byte-identically.
func buildRebuildDeltaDocs(items []rebuildSegItem) (hnswDocs, bm25Docs []searchengine.Document) {
	return buildRebuildDocs(items)
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
