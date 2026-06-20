// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mergeWait polls until at least one background merge completes or the deadline
// passes, returning the final MergeCount. The searchengine package's own
// waitForMerge is keyed to its mock query/stats types and is unexported, so this
// hnsw-package test re-authors the tiny poll loop locally.
func mergeWait(e *searchengine.SegmentedIndex[[]byte, struct{}]) uint64 {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e.MergeCount() >= 1 {
			return e.MergeCount()
		}
		time.Sleep(2 * time.Millisecond)
	}
	return e.MergeCount()
}

// allDeadMergedExport builds one sealed segment from N vecDocs, deletes every
// member so the dead ratio hits 1.0 (≥ DeletesPctAllowed), waits for the
// background merge to consolidate it into an empty (all-false-accept) segment,
// and returns that engine's exported blobs. The merged segment carries zero
// members, so its Encode() exercises the empty-graph serialization path.
func allDeadMergedExport(t *testing.T) []searchengine.SegmentBlob {
	t.Helper()
	const n = 16
	docs := vecDocs(n)

	eng := searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     8,       // batch of 16 seals into a segment
		DeletesPctAllowed:  0.33,    // 100% dead trips the dead-ratio trigger
		SegmentCountTarget: 1 << 30, // never the count-target trigger
	})
	defer eng.Close()

	if err := eng.Add(docs); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, d := range docs {
		eng.Delete(d.ID)
	}

	if got := mergeWait(eng); got < 1 {
		t.Fatalf("background merge of the all-dead segment never fired: MergeCount=%d", got)
	}

	blobs := eng.Export()
	if len(blobs) == 0 {
		t.Fatalf("Export returned no blobs after the all-dead merge")
	}
	return blobs
}

// TestAllDeadMergeReloadDecodes is the failing-repro-first gate for the
// empty-segment reload hazard: when every doc in an HNSW segment is deleted, the
// background merge consolidates it into a zero-member segment. Before the fix,
// encode() wrote a v1 version byte for that empty graph and decodeGraph rejected
// it on reload, so Importing the exported blobs into a fresh engine failed with
// "unsupported binary hnsw serial version: 1". With the always-v2 fix the empty
// segment encodes as a valid zero-node v2 blob, so the reload now succeeds and a
// search over a (now-gone) doc's vector returns an empty result set, not a crash.
func TestAllDeadMergeReloadDecodes(t *testing.T) {
	const n = 16
	docs := vecDocs(n)
	blobs := allDeadMergedExport(t)

	fresh := searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     8,
		DeletesPctAllowed:  0.33,
		SegmentCountTarget: 1 << 30,
	})
	defer fresh.Close()

	if err := fresh.Import(blobs, nil); err != nil {
		t.Fatalf("Import of an all-dead-merged segment failed: %v (the empty graph must round-trip as a valid v2 blob)", err)
	}

	// The reloaded engine loads cleanly; searching for a now-gone doc's vector
	// returns an empty result set (no members survived the all-dead merge).
	if hits := fresh.Search(docs[0].Vector, 10); len(hits) != 0 {
		t.Fatalf("reloaded all-dead engine Search = %v, want empty", hits)
	}
}

// TestAllDeadThenRecover proves the engine is not wedged by the all-dead state:
// after every doc is deleted and the background merge consolidates the segment to
// empty, adding a fresh batch of docs makes them searchable again (recall ≥ 0.90).
// Ticket Test 3 — recovery from the all-dead condition.
func TestAllDeadThenRecover(t *testing.T) {
	const n = 16
	docs := vecDocs(n)

	eng := searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     8,
		DeletesPctAllowed:  0.33,
		SegmentCountTarget: 1 << 30,
	})
	defer eng.Close()

	if err := eng.Add(docs); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, d := range docs {
		eng.Delete(d.ID)
	}
	if got := mergeWait(eng); got < 1 {
		t.Fatalf("background merge of the all-dead segment never fired: MergeCount=%d", got)
	}

	// Re-add a fresh, distinct batch (different seed + id prefix so it shares no id
	// or vector with the dead set) — the engine must index and search it normally.
	recovered := vecDocsSeed(n, 0x9e37, 0x79b9, "r")
	if err := eng.Add(recovered); err != nil {
		t.Fatalf("Add after all-dead: %v", err)
	}
	if err := eng.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if frac := topKRecall(eng.Search, recovered, 10); frac < 0.90 {
		t.Fatalf("post-recovery top-10 recall = %.3f, want >= 0.90", frac)
	}
}

// TestEmptyGraphRoundTrip pins the core format-layer guarantee: an empty HNSW
// segment encodes to a valid 29-byte zero-node v2 blob that decodes cleanly to an
// empty, searchable segment. This is the unit-level floor under the engine-level
// all-dead repro — both empty-segment producers (Merge and Build) feed it.
func TestEmptyGraphRoundTrip(t *testing.T) {
	// Merge over zero inputs yields an empty segment (one of the two producers).
	seg, err := Format{}.Merge(nil, nil)
	if err != nil {
		t.Fatalf("Merge(nil,nil): %v", err)
	}

	blob, err := seg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// [v2][7×uint32 header, nodeCount=0] = 1 + 28 = 29 bytes, no node records, no
	// trailing vectors.
	if len(blob) != 29 {
		t.Fatalf("empty-graph blob len = %d, want 29", len(blob))
	}
	if blob[0] != serialVersionWithVectors {
		t.Fatalf("empty-graph version byte = %d, want v%d", blob[0], serialVersionWithVectors)
	}

	decoded, err := Format{}.Decode(blob)
	if err != nil {
		t.Fatalf("Decode of empty v2 blob: %v", err)
	}
	if ids := decoded.IDs(); len(ids) != 0 {
		t.Fatalf("decoded empty segment IDs = %v, want empty", ids)
	}
	if hits := decoded.Search(make([]byte, defaultVecBytes), struct{}{}, 5, nil); len(hits) != 0 {
		t.Fatalf("decoded empty segment Search = %v, want empty", hits)
	}
}

// TestBuildEmptyBatchRoundTrip covers the SECOND empty-segment producer: an
// all-empty-vector Build batch is filtered to zero items (format.go), yielding an
// empty graph that must round-trip through Encode/Decode just like the merge
// producer. Guards the Build/seal path the merge-only repro would miss.
func TestBuildEmptyBatchRoundTrip(t *testing.T) {
	seg, err := Format{}.Build([]searchengine.Document{{ID: "x"}, {ID: "y"}})
	if err != nil {
		t.Fatalf("Build all-empty-vector batch: %v", err)
	}
	if ids := seg.IDs(); len(ids) != 0 {
		t.Fatalf("Build all-empty-vector batch indexed %v, want empty", ids)
	}

	blob, err := seg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if blob[0] != serialVersionWithVectors {
		t.Fatalf("Build-path empty blob version byte = %d, want v%d", blob[0], serialVersionWithVectors)
	}

	decoded, err := Format{}.Decode(blob)
	if err != nil {
		t.Fatalf("Decode of Build-path empty blob: %v", err)
	}
	if hits := decoded.Search(make([]byte, defaultVecBytes), struct{}{}, 5, nil); len(hits) != 0 {
		t.Fatalf("decoded Build-path empty segment Search = %v, want empty", hits)
	}
}
