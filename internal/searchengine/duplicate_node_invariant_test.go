package searchengine

import (
	"strings"
	"testing"
)

// TestNewEntryRejectsDuplicateGraphNodes asserts a segment's RAW node count
// equals its distinct member count, checked in newEntry.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG for this to go red: newEntry
// (engine.go). That question is the whole reason this gate lives here rather than
// over a test-local mock's Merge — the previous form's answer was mockFormat.Merge,
// which is not a production call, so a mock that deduplicated its own items satisfied
// it while every production format kept appending per copy.
//
// mockFormat.Merge deliberately does NOT deduplicate, which is what lets arm 1 drive
// a genuinely duplicate-producing merge into the production check.
func TestNewEntryRejectsDuplicateGraphNodes(t *testing.T) {
	// ARM 1 — A MERGE THAT PRODUCES DUPLICATES MUST ERROR, and must never be silently
	// repaired. A silent dedup would make this gate PERMANENTLY UNFALSIFIABLE: the
	// check would fix the condition it exists to detect, so it could never go red
	// again for any reason. That looks like robustness and is the more dangerous form
	// of vacuity.
	t.Run("a merge over constituents sharing ids is refused", func(t *testing.T) {
		const corpus = 32
		e := New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs:     corpus * 4,
			DeletesPctAllowed:  MergeDisabledDeadRatio,
			SegmentCountTarget: MergeDisabledCountTarget,
		})
		defer e.Close()

		ids, _ := ledgerIDs(corpus)
		// Two layers over the SAME ids with different content — the state two rebuilds
		// leave behind when nothing retired the first.
		oneSegmentPerLayer(t, e, ids, "layerA")
		oneSegmentPerLayer(t, e, ids, "layerB")

		before := e.Export()
		if len(before) != 2 {
			t.Fatalf("fixture wants 2 layered segments, got %d", len(before))
		}

		published, err := e.ReplaceBucket(0, ledgerBucketCount, []SegmentID{before[0].ID, before[1].ID}, nil, nil)
		if err == nil {
			t.Fatalf("a merge whose constituents share ids must be REFUSED, got a swap publishing %q", published)
		}
		if !strings.Contains(err.Error(), "must carry exactly one node per id") {
			t.Fatalf("expected the graph-equals-members invariant error, got: %v", err)
		}
		if published != "" {
			t.Fatalf("a refused swap must publish nothing, got %q", published)
		}
		// The refusal must leave the resident set untouched — a rejected swap is a
		// no-op, never a partial consumption of the constituents.
		if got := len(e.Export()); got != 2 {
			t.Fatalf("a refused swap must leave the constituents resident, got %d segments", got)
		}
	})

	// ARM 2 — A SEAL OVER A BATCH WITH A REPEATED ID MUST SUCCEED WITH ONE MEMBER.
	// This is LEGITIMATELY REACHABLE, not defensive coding: an id sitting in an
	// unsealed tail can also arrive in the next batch, and a drain seals the whole
	// buffer in ONE Build, so both copies reach the builder through an ordinary
	// sequence. Under a rejecting newEntry with no upstream normalisation that is a
	// hard error on the embed writeback seam.
	//
	// Without this arm the gate would be satisfied by a build path that simply
	// rejected everything.
	t.Run("a seal over a batch repeating an id succeeds with one member", func(t *testing.T) {
		e := New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs:     1024,
			DeletesPctAllowed:  MergeDisabledDeadRatio,
			SegmentCountTarget: MergeDisabledCountTarget,
		})
		defer e.Close()

		// "dup" appears TWICE in one batch, with different content — the shape a tail id
		// re-arriving in an embed batch produces. LAST-WINS, matching the route map's
		// own last-append-wins semantics.
		docs := []Document{
			doc("a", "tok alpha"),
			doc("dup", "tok first"),
			doc("b", "tok beta"),
			doc("dup", "tok second"),
		}
		if err := e.Add(docs); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := e.Flush(); err != nil {
			t.Fatalf("a seal over a batch repeating an id must SUCCEED — it is an ordinary write, not the defect: %v", err)
		}

		export := e.Export()
		if len(export) != 1 {
			t.Fatalf("the batch must seal into exactly one segment, got %d", len(export))
		}
		if got := export[0].DocCount; got != 3 {
			t.Fatalf("the sealed segment must hold 3 distinct ids (a, b, dup), got DocCount %d", got)
		}
		set := e.set.Load()
		entry := set.entryByID(export[0].ID)
		if entry == nil {
			t.Fatal("sealed segment not resident")
		}
		if _, ok := entry.members["dup"]; !ok {
			t.Fatal("the repeated id must be a member of the sealed segment")
		}
		if got := len(entry.members); got != 3 {
			t.Fatalf("the repeated id must contribute exactly ONE member, got %d members", got)
		}
	})
}
