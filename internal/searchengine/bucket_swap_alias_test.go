package searchengine

import "testing"

// bucket_swap_alias_test.go covers what happens when a consolidation publishes the
// SAME id one of the window's own segments already carried.
//
// A segment id is a content hash, so rebuilding a partition from the documents a
// freshly sealed segment holds encodes to the same bytes and lands the same id.
// Every id-keyed operation then treats the two as one object, and the two tests
// here pin the consequences a caller must not suffer: the id must be REPORTED so
// the caller can avoid retiring what it just published, and the reclaim hook must
// not name it as removed.

// aliasedReEmit reproduces one drain-then-tick cycle over a single partition and
// returns the sealed tail's id alongside the id the re-emit published.
//
// The mock format's merge-of-one reproduces its build bytes, so the two ids are
// equal — which is the whole point. The helper asserts that precondition, because
// a format that stopped aliasing would make both callers vacuous.
func aliasedReEmit(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], docs []Document) (tail, published SegmentID) {
	t.Helper()

	sealed, err := e.AddSealAndSupersede(docs)
	if err != nil {
		t.Fatalf("AddSealAndSupersede: %v", err)
	}
	if !sealed.Created {
		t.Fatal("the seal did not create a segment, so there is no tail for this fixture to retire")
	}
	tail = sealed.ID

	// The tick rebuilds the partition from the documents the tail holds. The tail is
	// excluded from the constituents, exactly as the drain path excludes it.
	published, err = e.ReplaceBucket(0, constituentsBucketCount, nil, docIDsOf(docs), docs)
	if err != nil {
		t.Fatalf("ReplaceBucket: %v", err)
	}
	if published != tail {
		t.Fatalf("precondition lost: published %q != tail %q — the alias this test exists for no longer occurs",
			published, tail)
	}
	return tail, published
}

func docIDsOf(docs []Document) []ExternalID {
	ids := make([]ExternalID, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids
}

// TestReEmitDoesNotUnloadWhatItPublished is the TOTAL-LOSS catcher. Retiring the
// window's sealed segments by id after a re-emit must not take the corpus with it.
//
// Unload removes BY ID and the rebuilt segment can carry the tail's id, so an
// unconditional Unload(tails) deletes the segment the re-emit just published —
// silently, because Unload reports nothing and the tick returns no error. Against
// the unfixed engine this leaves 0 documents in 0 segments.
func TestReEmitDoesNotUnloadWhatItPublished(t *testing.T) {
	const corpus = 4 // FIXTURE CONSTANT — never read back from the engine.

	e := bucketTestEngine(nil)
	defer e.Close()

	taken := map[string]bool{}
	docs := make([]Document, corpus)
	for i := range docs {
		docs[i] = doc(idForBucket(t, 0, taken), "alpha")
	}

	tail, published := aliasedReEmit(t, e, docs)

	// THE FIX: retire only the sealed segments the rebuild did not republish.
	e.Unload(retiringIDs([]SegmentID{tail}, []SegmentID{published}))

	if got := e.ResidentDocCount(); got != corpus {
		t.Fatalf("ResidentDocCount = %d after the re-emit, want %d — the tick retired its own output", got, corpus)
	}
	if segs := e.Export(); len(segs) != 1 {
		t.Fatalf("Export = %d segments, want 1 consolidated", len(segs))
	}
}

// retiringIDs mirrors the caller-side subtraction the drain path performs, so this
// test exercises the contract ReplaceBucket's reported id exists to support.
func retiringIDs(tails, published []SegmentID) []SegmentID {
	keep := make(map[SegmentID]bool, len(published))
	for _, id := range published {
		keep[id] = true
	}
	out := make([]SegmentID, 0, len(tails))
	for _, id := range tails {
		if !keep[id] {
			out = append(out, id)
		}
	}
	return out
}

// TestReclaimHookNeverReportsItsOwnMergedID is the SELF-RECLAIM catcher. The hook
// tells an owner which stored copies it may now delete, so naming the live
// segment's own id there instructs the owner to delete the segment currently
// serving reads.
//
// The two legs are both necessary. The CONTROL consolidates two distinct segments
// into genuinely new content and proves the hook fires and reports normally, so the
// catcher's silence cannot be mistaken for a dead fixture. The CATCHER consolidates
// a single segment, which reproduces that segment's own bytes and therefore its id:
// the correct outcome is that nothing is reported for reclaim at all, and the one
// forbidden outcome is reporting the live id.
func TestReclaimHookNeverReportsItsOwnMergedID(t *testing.T) {
	t.Run("control: a genuine consolidation reports its inputs", func(t *testing.T) {
		var fired []MergeResult
		e := bucketTestEngine(func(res MergeResult) { fired = append(fired, res) })
		defer e.Close()

		taken := map[string]bool{}
		first := doc(idForBucket(t, 0, taken), "alpha")
		second := doc(idForBucket(t, 0, taken), "beta")
		for _, d := range []Document{first, second} {
			if err := e.Add([]Document{d}); err != nil {
				t.Fatalf("Add: %v", err)
			}
		}
		before := e.Export()
		if len(before) != 2 {
			t.Fatalf("Export before = %d segments, want 2 distinct constituents", len(before))
		}

		published, err := e.ReplaceBucket(0, constituentsBucketCount,
			[]SegmentID{before[0].ID, before[1].ID}, nil, nil)
		if err != nil {
			t.Fatalf("ReplaceBucket: %v", err)
		}
		if len(fired) != 1 {
			t.Fatalf("the reclaim hook fired %d times, want 1 — the fixture cannot observe reclaims", len(fired))
		}
		if len(fired[0].Removed) != 2 {
			t.Fatalf("Removed = %d ids, want the 2 consolidated constituents", len(fired[0].Removed))
		}
		assertNoSelfReclaim(t, fired, published)
	})

	t.Run("catcher: consolidating one segment never reclaims the live id", func(t *testing.T) {
		var fired []MergeResult
		e := bucketTestEngine(func(res MergeResult) { fired = append(fired, res) })
		defer e.Close()

		taken := map[string]bool{}
		docs := []Document{
			doc(idForBucket(t, 0, taken), "alpha"),
			doc(idForBucket(t, 0, taken), "alpha beta"),
		}
		if err := e.Add(docs); err != nil {
			t.Fatalf("Add: %v", err)
		}
		before := e.Export()
		if len(before) != 1 {
			t.Fatalf("Export before = %d segments, want 1", len(before))
		}
		fired = nil

		// Merge-of-one reproduces the constituent's bytes, so the published id IS the
		// constituent's id — the aliasing shape.
		published, err := e.ReplaceBucket(0, constituentsBucketCount, []SegmentID{before[0].ID}, nil, nil)
		if err != nil {
			t.Fatalf("ReplaceBucket: %v", err)
		}
		if published != before[0].ID {
			t.Fatalf("precondition lost: published %q != constituent %q — this test's aliasing shape no longer occurs",
				published, before[0].ID)
		}
		assertNoSelfReclaim(t, fired, published)

		// And the segment is still there to serve reads.
		if got := e.ResidentDocCount(); got != len(docs) {
			t.Fatalf("ResidentDocCount = %d, want %d", got, len(docs))
		}
	})
}

func assertNoSelfReclaim(t *testing.T, fired []MergeResult, published SegmentID) {
	t.Helper()
	for _, res := range fired {
		for _, id := range res.Removed {
			if id == res.Merged.ID {
				t.Fatalf("the reclaim hook reported its own live segment %q inside Removed — "+
					"the owner would delete the stored copy of the segment it just published", id)
			}
			if id == published {
				t.Fatalf("the reclaim hook reported the published id %q inside Removed", id)
			}
		}
	}
}
