// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// vecDocs builds n searchengine.Documents with deterministic 32-byte vectors.
func vecDocs(n int) []searchengine.Document {
	return docsFromItems(randomVectors(n), "")
}

// vecDocsSeed builds n docs from a DISTINCT vector stream (different PCG seed) and
// prefixes ids so two corpora never share an id OR a vector.
func vecDocsSeed(n int, s1, s2 uint64, idPrefix string) []searchengine.Document {
	return docsFromItems(randomVectorsSeed(n, s1, s2), idPrefix)
}

func docsFromItems(items []binaryBuildItem, idPrefix string) []searchengine.Document {
	docs := make([]searchengine.Document, len(items))
	for i, it := range items {
		docs[i] = searchengine.Document{ID: idPrefix + it.id, Vector: it.vec}
	}
	return docs
}

// acceptExcept returns an accept predicate that rejects the named ids.
func acceptExcept(dead ...string) func(searchengine.ExternalID) bool {
	deadSet := make(map[string]bool, len(dead))
	for _, d := range dead {
		deadSet[d] = true
	}
	return func(id searchengine.ExternalID) bool { return !deadSet[id] }
}

// TestBuildSearchBasic builds a segment and asserts an exact-match query recovers
// its own id within the top-k for the overwhelming majority of a query sample.
// HNSW is APPROXIMATE — a single query can miss the exact NN — so the honest
// property is high recall@k over a sample, not deterministic single-query top-1.
func TestBuildSearchBasic(t *testing.T) {
	docs := vecDocs(500)
	seg, err := Format{}.Build(docs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Sample the whole corpus so the recall fraction is a stable estimate of the
	// ~97% mean (the build is deterministic, but per-node recall varies across the
	// corpus, so a small slice is noisy). Floor 0.93.
	const k = 5
	recovered := 0
	scoreOK := true
	for i := range docs {
		hits := seg.Search(docs[i].Vector, struct{}{}, k, nil)
		for _, h := range hits {
			if h.Score <= 0 || h.Score > 1.0 {
				scoreOK = false
			}
			if h.ID == docs[i].ID {
				recovered++
				break
			}
		}
	}
	if !scoreOK {
		t.Fatal("a hit had a score outside (0,1]")
	}
	if frac := float64(recovered) / float64(len(docs)); frac < 0.93 {
		t.Fatalf("exact-match recall@%d = %d/%d (%.3f), want >= 0.93", k, recovered, len(docs), frac)
	}
}

// TestSearchAcceptFiltersDeadID marks a subset dead via an accept predicate
// returning false for them and asserts Search never returns a dead id — even when
// that dead id's vector is the EXACT query (over-fetch-then-filter preserves
// recall for the live set). Criterion: Phase 2 Step 1.
func TestSearchAcceptFiltersDeadID(t *testing.T) {
	docs := vecDocs(800)
	seg, err := Format{}.Build(docs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	deadID := docs[100].ID
	accept := acceptExcept(deadID)

	// Query with the dead id's exact vector — its exact match is itself, but it is
	// dead so it must never appear.
	hits := seg.Search(docs[100].Vector, struct{}{}, 10, accept)
	for _, h := range hits {
		if h.ID == deadID {
			t.Fatalf("Search returned dead id %s even though accept rejects it", deadID)
		}
	}
	// And a live neighbor still comes back.
	if len(hits) == 0 {
		t.Fatal("Search returned nothing for a live set with one dead member")
	}
}

// TestBuildToleratesEmptyVectors builds over a doc slice mixing populated and
// empty Vector fields and asserts it indexes only the populated ones and does not
// panic. Criterion: Phase 2 Step 1 (formats tolerate absent data).
func TestBuildToleratesEmptyVectors(t *testing.T) {
	populated := vecDocs(10)
	mixed := make([]searchengine.Document, 0, 20)
	for i, d := range populated {
		mixed = append(mixed, d)
		// Interleave empty-vector docs.
		mixed = append(mixed, searchengine.Document{ID: "empty" + d.ID + string(rune('a'+i))})
	}

	seg, err := Format{}.Build(mixed)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ids := seg.IDs()
	if len(ids) != len(populated) {
		t.Fatalf("indexed %d docs, want %d (empty-vector docs must be skipped)", len(ids), len(populated))
	}
	for _, id := range ids {
		if len(id) >= 5 && id[:5] == "empty" {
			t.Fatalf("empty-vector doc %s was indexed", id)
		}
	}

	// Empty batch must not panic and yields a searchable zero-hit segment.
	emptySeg, err := Format{}.Build([]searchengine.Document{{ID: "x"}, {ID: "y"}})
	if err != nil {
		t.Fatalf("Build all-empty: %v", err)
	}
	if got := emptySeg.Search(make([]byte, defaultVecBytes), struct{}{}, 5, nil); len(got) != 0 {
		t.Fatalf("all-empty segment Search = %v, want empty", got)
	}
}

// TestEncodeDecodeRoundTrip asserts a built segment survives Encode→Decode with
// identical search results and merge-eligibility. Criterion: Phase 2 Step 1
// (Decode reconstructs the same concrete type).
func TestEncodeDecodeRoundTrip(t *testing.T) {
	docs := vecDocs(400)
	seg, err := Format{}.Build(docs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	blob, err := seg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Format{}.Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := decoded.(*hnswSegment); !ok {
		t.Fatalf("Decode returned %T, want *hnswSegment", decoded)
	}
	for i := range 50 {
		o := seg.Search(docs[i].Vector, struct{}{}, 5, nil)
		d := decoded.Search(docs[i].Vector, struct{}{}, 5, nil)
		if !sameHits(o, d) {
			t.Fatalf("query %d: decoded hits differ from original\n orig=%v\n dec =%v", i, o, d)
		}
	}
}

// TestAggregateStatsNoOp asserts AggregateStats returns the zero struct{}.
func TestAggregateStatsNoOp(t *testing.T) {
	_ = Format{}.AggregateStats(nil) // returns struct{}{}; compiles ⇒ correct shape.
}

// TestMergeUnionOfLiveMembers builds two segments, deletes some members via
// accept predicates, Merges them, and asserts the consolidated segment indexes
// exactly the union of live members and returns correct nearest neighbors. A
// second case decodes both inputs from Encode() bytes first (proving decoded
// segments are merge-eligible) and asserts the same consolidated membership.
// Asserts RECALL/membership, NOT byte-identity: Merge re-inserts the surviving
// members of two segments into a fresh graph, so the merged blob is not expected to
// equal either input's bytes — membership + recall is the meaningful merge property
// (the per-segment build is itself deterministic, but that is not what Merge tests).
func TestMergeUnionOfLiveMembers(t *testing.T) {
	docsA := vecDocs(300)
	// Distinct ids AND distinct vectors for B (different PCG seed) so no A/B pair
	// shares a vector — otherwise an A query would tie with its B twin.
	docsB := vecDocsSeed(300, 0x9e37, 0x79b9, "b")

	segA, err := Format{}.Build(docsA)
	if err != nil {
		t.Fatalf("Build A: %v", err)
	}
	segB, err := Format{}.Build(docsB)
	if err != nil {
		t.Fatalf("Build B: %v", err)
	}

	// Kill 50 from A, 30 from B.
	deadA := idsOf(docsA[:50])
	deadB := idsOf(docsB[:30])
	acceptA := acceptExcept(deadA...)
	acceptB := acceptExcept(deadB...)

	assertMerge := func(t *testing.T, a, b searchengine.Segment[[]byte, struct{}]) {
		merged, err := Format{}.Merge(
			[]searchengine.Segment[[]byte, struct{}]{a, b},
			[]func(searchengine.ExternalID) bool{acceptA, acceptB},
		)
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}

		wantLive := make(map[string]bool)
		for _, d := range docsA[50:] {
			wantLive[d.ID] = true
		}
		for _, d := range docsB[30:] {
			wantLive[d.ID] = true
		}

		got := merged.IDs()
		if len(got) != len(wantLive) {
			t.Fatalf("merged indexes %d members, want %d (union of live)", len(got), len(wantLive))
		}
		for _, id := range got {
			if !wantLive[id] {
				t.Fatalf("merged contains %s which should be dead/excluded", id)
			}
		}

		// Nearest-neighbor correctness over the consolidated graph: a live member's
		// exact-match query recovers itself within the top-k for the bulk of a
		// sample (HNSW is approximate — assert recall, not deterministic top-1), and
		// NO excluded/dead id is ever returned (the strict Merge-correctness check).
		recovered := 0
		liveA := docsA[50:] // all live (we killed docsA[:50])
		for _, liveQ := range liveA {
			hits := merged.Search(liveQ.Vector, struct{}{}, 5, nil)
			found := false
			for _, h := range hits {
				if !wantLive[h.ID] {
					t.Fatalf("merged Search returned excluded id %s", h.ID)
				}
				if h.ID == liveQ.ID {
					found = true
				}
			}
			if found {
				recovered++
			}
		}
		if frac := float64(recovered) / float64(len(liveA)); frac < 0.93 {
			t.Fatalf("merged exact-match recall@5 = %d/%d (%.3f), want >= 0.93", recovered, len(liveA), frac)
		}
	}

	t.Run("built-inputs", func(t *testing.T) { assertMerge(t, segA, segB) })

	t.Run("decoded-inputs", func(t *testing.T) {
		ba, _ := segA.Encode()
		bb, _ := segB.Encode()
		da, err := Format{}.Decode(ba)
		if err != nil {
			t.Fatalf("decode A: %v", err)
		}
		db, err := Format{}.Decode(bb)
		if err != nil {
			t.Fatalf("decode B: %v", err)
		}
		assertMerge(t, da, db)
	})
}

func idsOf(docs []searchengine.Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}

func sameHits(a, b []searchengine.Hit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Score != b[i].Score {
			return false
		}
	}
	return true
}

// sortedLiveIDs runs a broad search and returns the sorted set of returned ids —
// a recall proxy for the engine integration test.
func sortedLiveIDs(hits []searchengine.Hit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	sort.Strings(ids)
	return ids
}
