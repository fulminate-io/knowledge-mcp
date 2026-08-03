package searchengine

import (
	"fmt"
	"testing"
)

// TestAddSealAndSupersedeDuplicateSeal covers re-adding a batch whose content is
// byte-identical to what is already resident.
//
// The seal dedupes by content hash, so an identical batch resolves to the
// segment that is ALREADY resident rather than publishing a new one — while the
// supersede half retires the pre-seal copies regardless. Both halves believe
// they succeeded, and the id ends up live nowhere.
//
// THE SEARCHABLE SET IS THE ASSERTION, not the counts. Measured at HEAD the
// segment count and the distinct-doc count both stay healthy while the corpus
// answers nothing, so a test asserting only counts passes against the defect.
func TestAddSealAndSupersedeDuplicateSeal(t *testing.T) {
	// MinSegmentDocs far above the batch so nothing self-seals mid-fixture.
	const bigMinSeg = 1 << 20

	buildDocs := func(content string) []Document {
		docs := make([]Document, 0, 20)
		for i := range 20 {
			docs = append(docs, doc(fmt.Sprintf("n-%d", i), fmt.Sprintf("%s n-%d", content, i)))
		}
		return docs
	}

	t.Run("identical_re_add_keeps_corpus_searchable", func(t *testing.T) {
		e := newTestEngine(bigMinSeg)
		defer e.Close()
		docs := buildDocs("alpha beta")

		if _, err := e.AddSealAndSupersede(docs); err != nil {
			t.Fatalf("first AddSealAndSupersede: %v", err)
		}
		// PRECONDITION: the corpus answers before the duplicate seal, so a fixture
		// that never indexed anything cannot pass this leg.
		if hits := e.Search(mockQuery{term: "alpha"}, 1000); len(hits) != 20 {
			t.Fatalf("precondition: Search(alpha) = %d hits, want 20", len(hits))
		}

		if _, err := e.AddSealAndSupersede(docs); err != nil {
			t.Fatalf("duplicate AddSealAndSupersede: %v", err)
		}

		if hits := e.Search(mockQuery{term: "alpha"}, 1000); len(hits) != 20 {
			t.Fatalf("re-adding an identical batch wiped the searchable corpus: "+
				"Search(alpha) = %d hits, want 20", len(hits))
		}
		if segs := e.Export(); len(segs) != 1 {
			t.Fatalf("Export = %d segments, want 1", len(segs))
		}
		if got := e.DistinctResidentDocCount(); got != 20 {
			t.Fatalf("DistinctResidentDocCount = %d, want 20", got)
		}
	})

	t.Run("delete_then_identical_re_add_revives_id", func(t *testing.T) {
		e := newTestEngine(bigMinSeg)
		defer e.Close()
		docs := buildDocs("alpha beta")

		if _, err := e.AddSealAndSupersede(docs); err != nil {
			t.Fatalf("first AddSealAndSupersede: %v", err)
		}
		e.Delete("n-7")
		// PRECONDITION: the delete actually took, so a fixture that deleted nothing
		// cannot pass this leg.
		if hits := e.Search(mockQuery{term: "alpha"}, 1000); len(hits) != 19 {
			t.Fatalf("precondition: after Delete, Search(alpha) = %d hits, want 19", len(hits))
		}

		if _, err := e.AddSealAndSupersede(docs); err != nil {
			t.Fatalf("duplicate AddSealAndSupersede: %v", err)
		}

		if hits := e.Search(mockQuery{term: "alpha"}, 1000); len(hits) != 20 {
			t.Fatalf("re-adding the deleted id did not revive it: "+
				"Search(alpha) = %d hits, want 20", len(hits))
		}
		// The revived id must be findable by its OWN distinctive term, not merely
		// counted — a count alone cannot tell n-7 from a duplicate of some sibling.
		hits := e.Search(mockQuery{term: "n-7"}, 10)
		if len(hits) != 1 || hits[0].ID != "n-7" {
			t.Fatalf("Search(n-7) = %v, want exactly the revived id", searchIDs(hits))
		}
	})

	t.Run("perturbed_re_add_still_retires_old_copy", func(t *testing.T) {
		// CHARACTERIZATION GUARD — green before and after the fix. It is NOT
		// red-first. It exists so the other two legs cannot be made green by
		// disabling the kill: a genuinely-new payload must still retire the copy
		// it supersedes.
		e := newTestEngine(bigMinSeg)
		defer e.Close()

		if _, err := e.AddSealAndSupersede(buildDocs("alpha beta")); err != nil {
			t.Fatalf("first AddSealAndSupersede: %v", err)
		}
		// Same 20 ids, DIFFERENT content — hashes to a new segment, so this one
		// genuinely publishes.
		if _, err := e.AddSealAndSupersede(buildDocs("alpha gamma")); err != nil {
			t.Fatalf("perturbed AddSealAndSupersede: %v", err)
		}

		if hits := e.Search(mockQuery{term: "alpha"}, 1000); len(hits) != 20 {
			t.Fatalf("Search(alpha) = %d hits, want exactly 20 — one live copy per id", len(hits))
		}
		if got := e.DistinctResidentDocCount(); got != 20 {
			t.Fatalf("DistinctResidentDocCount = %d, want 20", got)
		}
		// The OLD payload must be gone. This is the supersede actually working.
		if stale := e.Search(mockQuery{term: "beta"}, 1000); len(stale) != 0 {
			t.Fatalf("the superseded payload is still live: Search(beta) = %v", searchIDs(stale))
		}
	})
}
