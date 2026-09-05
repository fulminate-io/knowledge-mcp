package searchengine

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// mergeWaitTimeout bounds how long a test waits for the background merge to fire.
const mergeWaitTimeout = 2 * time.Second

// waitForMerge polls until at least one background merge completes, and FAILS the
// test naming the deadline when none ever does.
//
// A WAIT THAT CANNOT BE SATISFIED IS A RED, NOT A SILENT TAX. It used to return
// the current count on timeout, which left it to each caller to notice the zero —
// and the caller that did not notice (TestMergeUnderRead, whose only assertion is
// a search result that holds whether or not a merge fires) sat through the whole
// deadline and passed anyway. Failing HERE makes every call site correct by
// construction instead of by its author's diligence.
func waitForMerge(t testing.TB, e *SegmentedIndex[mockQuery, mockStats]) {
	t.Helper()
	deadline := time.Now().Add(mergeWaitTimeout)
	for time.Now().Before(deadline) {
		if e.MergeCount() >= 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no background merge fired within %s (MergeCount still %d) — "+
		"the engine's merge trigger is disarmed or the occasion never arrived", mergeWaitTimeout, e.MergeCount())
}

// mergeEngine builds an engine whose merge fires readily: one doc per segment,
// and a dead-ratio trigger at the contract default.
func mergeEngine(t testing.TB, deletesPct float64, countTarget int) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	return closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  deletesPct,
		SegmentCountTarget: countTarget,
	}))
}

// TestMergeReclaimsDead deletes enough docs to push a segment past the dead
// ratio; the background merge must consolidate it into a fresh all-live segment,
// MergeCount increments, and search results are unchanged.
func TestMergeReclaimsDead(t *testing.T) {
	e := mergeEngine(t, 0.33, 1<<30)
	defer e.Close()

	// Build one segment of 4 docs (MinSegmentDocs=1 seals each Add, so add a
	// batch to get one multi-doc segment).
	if err := e.Add([]Document{
		doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x"),
	}); err != nil {
		t.Fatal(err)
	}
	// Delete 2 of 4 → 50% dead ≥ 0.33 → merge eligible.
	e.Delete("a")
	e.Delete("b")

	waitForMerge(t, e)

	// After merge: a fresh all-live segment with only the live docs.
	got := searchIDs(e.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[c d]" {
		t.Fatalf("post-merge search = %v, want [c d]", got)
	}
	if dr := e.Metrics().DeadRatio; dr != 0 {
		t.Fatalf("post-merge DeadRatio = %v, want 0 (all-live consolidated segment)", dr)
	}
}

// TestMergeUnderRead runs continuous searches while a merge swaps segments,
// asserting the read path stays stable (no panic, results always the live set).
func TestMergeUnderRead(t *testing.T) {
	e := mergeEngine(t, 0.33, 1<<30)
	defer e.Close()
	if err := e.Add([]Document{
		doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x"),
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		// Hammer Search across the merge swap: it must never panic and must only
		// ever return ids from the indexed corpus (a/b before delete, c/d after).
		deadline := time.Now().Add(500 * time.Millisecond)
		valid := map[ExternalID]bool{"a": true, "b": true, "c": true, "d": true}
		for time.Now().Before(deadline) {
			for _, h := range e.Search(mockQuery{term: "x"}, 10) {
				if !valid[h.ID] {
					t.Errorf("Search returned unexpected id %q during merge", h.ID)
				}
			}
		}
		close(done)
	}()

	e.Delete("a")
	e.Delete("b")
	waitForMerge(t, e)
	<-done

	got := searchIDs(e.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[c d]" {
		t.Fatalf("post-merge-under-read search = %v, want [c d]", got)
	}
}

// TestMergePulledSegment proves a DECODED/PULLED segment is merge-eligible
// exactly like a locally-built one: export → import into a fresh engine →
// delete past the ratio → background merge consolidates the decoded segment.
func TestMergePulledSegment(t *testing.T) {
	src := newTestEngine(t, 1)
	src.Add([]Document{doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x")})
	blobs := src.Export()
	src.Close()

	dst := mergeEngine(t, 0.33, 1<<30)
	defer dst.Close()
	if err := dst.Import(blobs, nil); err != nil {
		t.Fatal(err)
	}
	// The imported (decoded) segment now holds the indexed data with no source
	// Documents. Delete past the ratio and let merge consolidate it.
	dst.Delete("a")
	dst.Delete("b")

	waitForMerge(t, dst)
	got := searchIDs(dst.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[c d]" {
		t.Fatalf("post-merge pulled-segment search = %v, want [c d]", got)
	}
	if dr := dst.Metrics().DeadRatio; dr != 0 {
		t.Fatalf("post-merge DeadRatio = %v, want 0", dr)
	}
}

// TestMergeSegmentCountTarget triggers merge by segment count rather than dead
// ratio: many tiny segments collapse toward the target.
func TestMergeSegmentCountTarget(t *testing.T) {
	e := mergeEngine(t, 2.0, 4) // dead-ratio never fires; count target = 4
	defer e.Close()
	for i := range 10 {
		if err := e.Add([]Document{doc(fmt.Sprintf("d%d", i), "x")}); err != nil {
			t.Fatal(err)
		}
	}
	waitForMerge(t, e)
	// All 10 docs still searchable after consolidation.
	got := e.Search(mockQuery{term: "x"}, 20)
	ids := searchIDs(got)
	sort.Strings(ids)
	if len(ids) != 10 {
		t.Fatalf("post-count-merge search has %d ids, want 10", len(ids))
	}
}
