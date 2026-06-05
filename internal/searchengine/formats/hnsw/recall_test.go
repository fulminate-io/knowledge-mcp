// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// bruteForceTopK returns the exact top-k nearest external ids for query by a full
// Hamming scan — the validation oracle the approximate index is measured against.
func bruteForceTopK(items []binaryBuildItem, query []byte, k int) []string {
	type pair struct {
		id   string
		dist float32
	}
	ps := make([]pair, len(items))
	for i, it := range items {
		ps[i] = pair{id: it.id, dist: hammingDistance(query, it.vec)}
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].dist != ps[j].dist {
			return ps[i].dist < ps[j].dist
		}
		return ps[i].id < ps[j].id
	})
	out := make([]string, 0, k)
	for i := 0; i < k && i < len(ps); i++ {
		out = append(out, ps[i].id)
	}
	return out
}

// recallAtK returns the fraction of the exact top-k recovered by got.
func recallAtK(truth, got []string) float64 {
	if len(truth) == 0 {
		return 1
	}
	want := make(map[string]bool, len(truth))
	for _, id := range truth {
		want[id] = true
	}
	hit := 0
	for _, id := range got {
		if want[id] {
			hit++
		}
	}
	return float64(hit) / float64(len(truth))
}

// chunkSegments splits items into ceil(n/perSeg) hnswSegments, each built at the
// given efSearch — the multi-segment side of the parity harness.
func chunkSegments(t *testing.T, items []binaryBuildItem, perSeg, ef int) []*hnswSegment {
	t.Helper()
	var segs []*hnswSegment
	for i := 0; i < len(items); i += perSeg {
		end := min(i+perSeg, len(items))
		docs := make([]searchengine.Document, 0, end-i)
		for _, it := range items[i:end] {
			docs = append(docs, searchengine.Document{ID: it.id, Vector: it.vec})
		}
		seg, err := Format{}.Build(docs)
		if err != nil {
			t.Fatalf("Build segment: %v", err)
		}
		hs := seg.(*hnswSegment)
		hs.graph.setEfSearch(ef)
		segs = append(segs, hs)
	}
	return segs
}

// fanoutSearch mirrors the engine's cross-segment search: search every segment at
// k, then merge by score desc (ties by id) and take the global top-k. Uses the
// REAL hnswSegment.Search — the same per-segment path the engine drives.
func fanoutSearch(segs []*hnswSegment, query []byte, k int) []string {
	var all []searchengine.Hit
	for _, s := range segs {
		all = append(all, s.Search(query, struct{}{}, k, nil)...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].ID < all[j].ID
	})
	out := make([]string, 0, k)
	for i := 0; i < k && i < len(all); i++ {
		out = append(out, all[i].ID)
	}
	return out
}

// TestRecallParitySegmentedVsSingleGraph is the first-class success criterion: a
// multi-segment fan-out under the real HNSW format must match a single big-graph
// baseline's recall@10 within 0.02, across an efSearch sweep, measured against the
// exact-Hamming ground truth. The corpus (4096 vectors) split at perSeg=512 seals
// >= 8 segments so the multi-segment fan-out is genuinely exercised (the default
// 1024 would seal too few segments and false-green the parity check).
func TestRecallParitySegmentedVsSingleGraph(t *testing.T) {
	const (
		corpus  = 4096
		queries = 300
		perSeg  = 512 // 4096/512 = 8 segments
		k       = 10
	)
	items := randomVectors(corpus)
	// Queries: a disjoint deterministic vector stream so they are NOT exact corpus
	// members (a realistic "novel query" recall measurement, not self-match).
	qItems := randomVectorsSeed(queries, 0x5151, 0xA2A2)

	for _, ef := range []int{50, 100, 200} {
		// Single big-graph baseline at this ef.
		baseline := buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)
		baseline.setEfSearch(ef)

		// Multi-segment set at the same ef.
		segs := chunkSegments(t, items, perSeg, ef)
		if len(segs) < 8 {
			t.Fatalf("ef=%d: sealed %d segments, want >= 8 for real fan-out", ef, len(segs))
		}

		var baseRecall, segRecall float64
		for _, q := range qItems {
			truth := bruteForceTopK(items, q.vec, k)

			bHits := baseline.search(q.vec, k, nil)
			bGot := make([]string, len(bHits))
			for i, h := range bHits {
				bGot[i] = h.externalID
			}
			baseRecall += recallAtK(truth, bGot)

			segGot := fanoutSearch(segs, q.vec, k)
			segRecall += recallAtK(truth, segGot)
		}
		baseRecall /= float64(len(qItems))
		segRecall /= float64(len(qItems))

		t.Logf("ef=%-3d segments=%d  baseline recall@%d=%.4f  segmented recall@%d=%.4f  delta=%.4f",
			ef, len(segs), k, baseRecall, k, segRecall, baseRecall-segRecall)

		// The multi-segment fan-out searches EVERY segment and merges — it should
		// match (or slightly EXCEED, since each small segment is exhaustively
		// reachable) the single-graph baseline. Assert within 0.02 below baseline.
		if segRecall < baseRecall-0.02 {
			t.Fatalf("ef=%d: segmented recall@%d %.4f is more than 0.02 below baseline %.4f",
				ef, k, segRecall, baseRecall)
		}
	}
}

// TestRecallNoRegressionAcrossMerge logs the segment count (>= 4 before any merge)
// and asserts format.Merge's re-insertion produces a consolidated graph AS GOOD AS
// a from-scratch single-graph build over the same vectors (recall@10 within 0.02).
// It drives the SAME format.Merge the engine's background merger calls
// (merge.go:doMerge) with the same accept-filter shape, so the merge path is
// faithfully exercised without depending on background-goroutine timing.
//
// IMPORTANT measurement note: the pre-merge MULTI-segment fan-out (8 small graphs
// each searched at ef) has HIGHER recall than ANY single graph at the same ef
// (small graphs are more exhaustively reachable). So "post-merge vs pre-merge
// fan-out" is the WRONG oracle — consolidating 8→1 inherently moves to single-graph
// recall. The honest "Merge does not lose recall" property is: the merged
// (re-inserted) graph matches a graph BUILT FROM SCRATCH over the same vectors.
func TestRecallNoRegressionAcrossMerge(t *testing.T) {
	const (
		corpus  = 4096
		queries = 200
		perSeg  = 512
		k       = 10
		ef      = 50
	)
	items := randomVectors(corpus)
	qItems := randomVectorsSeed(queries, 0x3333, 0x4444)

	// Multi-segment set (>= 4 segments before merge), all members live.
	segs := chunkSegments(t, items, perSeg, ef)
	if len(segs) < 4 {
		t.Fatalf("pre-merge segment count = %d, want >= 4", len(segs))
	}

	// Merge all segments into one — nil accept admits everyone, exactly as the
	// engine merges an all-live set. format.Merge re-inserts the live vectors into a
	// fresh consolidated graph.
	ifaceSegs := make([]searchengine.Segment[[]byte, struct{}], len(segs))
	accept := make([]func(searchengine.ExternalID) bool, len(segs))
	for i, s := range segs {
		ifaceSegs[i] = s
	}
	mergedIface, err := Format{}.Merge(ifaceSegs, accept)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	merged := mergedIface.(*hnswSegment)
	merged.graph.setEfSearch(ef)

	if got := len(merged.IDs()); got != corpus {
		t.Fatalf("merged segment indexes %d members, want %d (union of all live)", got, corpus)
	}

	// From-scratch single-graph build over the SAME vectors — the oracle for "merge
	// re-insertion is as good as a fresh build".
	fresh := buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)
	fresh.setEfSearch(ef)

	var mergedRecall, freshRecall float64
	for _, q := range qItems {
		truth := bruteForceTopK(items, q.vec, k)
		mergedRecall += recallAtK(truth, searchHitIDs(merged.Search(q.vec, struct{}{}, k, nil)))
		fGot := fresh.search(q.vec, k, nil)
		fIDs := make([]string, len(fGot))
		for i, h := range fGot {
			fIDs[i] = h.externalID
		}
		freshRecall += recallAtK(truth, fIDs)
	}
	mergedRecall /= float64(len(qItems))
	freshRecall /= float64(len(qItems))

	t.Logf("merge: segments %d -> 1  recall@%d merged=%.4f from-scratch-baseline=%.4f delta=%.4f",
		len(segs), k, mergedRecall, freshRecall, freshRecall-mergedRecall)

	// Merge (re-insertion) must produce a graph as good as a fresh build.
	if mergedRecall < freshRecall-0.02 {
		t.Fatalf("merged-graph recall@%d %.4f is more than 0.02 below a from-scratch build %.4f — Merge lost recall",
			k, mergedRecall, freshRecall)
	}
}

func searchHitIDs(hits []searchengine.Hit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}
