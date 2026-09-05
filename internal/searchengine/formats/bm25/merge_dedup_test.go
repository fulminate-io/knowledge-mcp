package bm25

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBM25MergeKeepsOneNodePerID is the BM25 leg of the duplicate-node defect,
// named as its own format.
//
// WHY IT EXISTS ALONGSIDE THE newEntry INVARIANT. That check catches the violation
// format-agnostically at construction, but it cannot say WHICH format produced it, so
// a fix landing on hnsw alone would leave this arm broken while the shared gate
// reported the failure. mergeSegment appends a member slot per surviving copy, so two
// constituents carrying the same id previously yielded two slots for it — the index
// holding two docs for one id while the engine's route map records one.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG for this to go red: Format.Merge in
// this package. It is driven directly, with no mock in the path.
func TestBM25MergeKeepsOneNodePerID(t *testing.T) {
	var f Format

	// Two segments over the SAME ids with DIFFERENT text — the two-layer shape. The
	// differing text matters: identical content would collapse anyway and the test
	// would not discriminate.
	segA, _, err := f.Build([]searchengine.Document{
		{ID: "shared-1", Fields: map[string]string{searchengine.FieldContent: "alpha layer one"}},
		{ID: "shared-2", Fields: map[string]string{searchengine.FieldContent: "beta layer one"}},
		{ID: "only-a", Fields: map[string]string{searchengine.FieldContent: "gamma only a"}},
	})
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	segB, _, err := f.Build([]searchengine.Document{
		{ID: "shared-1", Fields: map[string]string{searchengine.FieldContent: "alpha layer two"}},
		{ID: "shared-2", Fields: map[string]string{searchengine.FieldContent: "beta layer two"}},
		{ID: "only-b", Fields: map[string]string{searchengine.FieldContent: "delta only b"}},
	})
	if err != nil {
		t.Fatalf("build B: %v", err)
	}

	merged, err := mergeSegments(t,
		[]searchengine.Segment[Query, *CorpusStats]{segA, segB},
		[]func(searchengine.ExternalID) bool{nil, nil},
	)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// IDs() is the RAW member list — what the merged index actually holds — so
	// counting occurrences in it is what sees a per-copy append. Counting distinct ids
	// instead would be the identity that holds however duplicated the segment is.
	counts := map[searchengine.ExternalID]int{}
	for _, id := range merged.IDs() {
		counts[id]++
	}
	for _, id := range []searchengine.ExternalID{"shared-1", "shared-2"} {
		if counts[id] != 1 {
			t.Errorf("id %q appears %d times in the merged segment — a merge over constituents sharing an id must keep exactly ONE member slot for it", id, counts[id])
		}
	}
	for _, id := range []searchengine.ExternalID{"only-a", "only-b"} {
		if counts[id] != 1 {
			t.Errorf("id %q appears %d times — an id carried by a single constituent must survive exactly once", id, counts[id])
		}
	}
	if got := len(merged.IDs()); got != 4 {
		t.Errorf("merged segment holds %d member slots, want 4 (the distinct union) — a per-copy append reports 6", got)
	}

	// The surviving copy must be SEARCHABLE, not merely present: a dedup that dropped
	// the postings along with the duplicate slot would satisfy every count above.
	stats := f.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{merged})
	hits := merged.Search(NewQuery("two"), stats, 10, nil)
	found := map[searchengine.ExternalID]bool{}
	for _, h := range hits {
		found[h.ID] = true
	}
	if !found["shared-1"] || !found["shared-2"] {
		t.Errorf("the surviving copy must keep its postings: searching the LAST layer's text returned %v", hits)
	}
}
