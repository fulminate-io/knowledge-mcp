package searchengine

import (
	"sync"
	"testing"
	"time"
)

// captureOnMerge is a concurrency-safe recorder of every MergeResult the engine
// fires, used to assert the callback contract without racing the merger goroutine.
type captureOnMerge struct {
	mu      sync.Mutex
	results []MergeResult
}

func (c *captureOnMerge) fn() OnMergeFunc {
	return func(r MergeResult) {
		c.mu.Lock()
		c.results = append(c.results, r)
		c.mu.Unlock()
	}
}

func (c *captureOnMerge) snapshot() []MergeResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]MergeResult, len(c.results))
	copy(out, c.results)
	return out
}

// callbackMergeEngine mirrors mergeEngine (one doc per segment, low triggers) but
// installs the supplied OnMerge so the callback path runs.
func callbackMergeEngine(t testing.TB, onMerge OnMergeFunc, deletesPct float64, countTarget int) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	return closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  deletesPct,
		SegmentCountTarget: countTarget,
		OnMerge:            onMerge,
	}))
}

// TestOnMergeFiresOncePerMerge asserts that a dead-ratio-triggered merge invokes
// OnMerge exactly once, with Removed = the superseded constituent ids and
// Merged.ID present in the post-merge Export() set.
func TestOnMergeFiresOncePerMerge(t *testing.T) {
	recorder := &captureOnMerge{}
	e := callbackMergeEngine(t, recorder.fn(), 0.33, 1<<30)
	defer e.Close()

	// One segment of 4 docs; record its id before the merge so we can assert it is
	// the superseded constituent.
	if err := e.Add([]Document{
		doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x"),
	}); err != nil {
		t.Fatal(err)
	}
	preMerge := e.Export()
	if len(preMerge) != 1 {
		t.Fatalf("pre-merge Export = %d blobs, want 1", len(preMerge))
	}
	constituentID := preMerge[0].ID

	// Delete 2 of 4 → 50% dead ≥ 0.33 → merge eligible.
	e.Delete("a")
	e.Delete("b")

	waitForMerge(t, e)
	// Settle: the callback fires AFTER mergeCnt.Add, so a brief poll bounds the gap
	// between MergeCount>=1 and the recorded result without a fixed sleep.
	results := pollResults(recorder, 1)

	if len(results) != 1 {
		t.Fatalf("OnMerge fired %d times, want exactly 1", len(results))
	}
	r := results[0]
	if len(r.Removed) != 1 || r.Removed[0] != constituentID {
		t.Fatalf("Removed = %v, want [%s] (the superseded constituent)", r.Removed, constituentID)
	}

	// Merged.ID must be the consolidated segment now in Export().
	post := e.Export()
	if len(post) != 1 {
		t.Fatalf("post-merge Export = %d blobs, want 1 (consolidated)", len(post))
	}
	if r.Merged.ID != post[0].ID {
		t.Fatalf("Merged.ID = %s, want %s (the published consolidated segment)", r.Merged.ID, post[0].ID)
	}
	if r.Merged.ID == constituentID {
		t.Fatal("Merged.ID equals the superseded constituent id — a merge must produce a NEW content hash")
	}
	if len(r.Merged.Bytes) == 0 {
		t.Fatal("Merged.Bytes is empty — the consolidated blob must carry the encoded segment")
	}
	if wantFmt := (mockFormat{}).Name(); r.Merged.Format != wantFmt {
		t.Fatalf("Merged.Format = %q, want %q", r.Merged.Format, wantFmt)
	}
}

// TestOnMergeCountTargetRemovesAllConstituents asserts a count-target merge
// (which consolidates EVERY entry) reports all the superseded ids in Removed.
func TestOnMergeCountTargetRemovesAllConstituents(t *testing.T) {
	recorder := &captureOnMerge{}
	e := callbackMergeEngine(t, recorder.fn(), 2.0, 4) // dead-ratio never fires; count target = 4
	defer e.Close()

	for _, id := range []string{"d0", "d1", "d2", "d3", "d4"} {
		if err := e.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	// 5 single-doc segments > target 4 → merge consolidates all 5.
	preIDs := map[SegmentID]bool{}
	for _, b := range e.Export() {
		preIDs[b.ID] = true
	}
	if len(preIDs) != 5 {
		t.Fatalf("pre-merge has %d segments, want 5", len(preIDs))
	}

	waitForMerge(t, e)
	results := pollResults(recorder, 1)
	if len(results) < 1 {
		t.Fatalf("OnMerge fired %d times, want >= 1", len(results))
	}
	// The first merge consolidated all 5 pre-merge segments: its Removed set must
	// equal exactly the 5 pre-merge ids.
	r := results[0]
	if len(r.Removed) != 5 {
		t.Fatalf("Removed has %d ids, want 5 (all constituents)", len(r.Removed))
	}
	for _, id := range r.Removed {
		if !preIDs[id] {
			t.Fatalf("Removed carries id %s not in the pre-merge set", id)
		}
	}
}

// TestOnMergeNotFiredWithoutTargets asserts a no-op tick (no merge-eligible
// segments) fires zero callbacks: a single all-live segment under both triggers
// never merges, so OnMerge must never run.
func TestOnMergeNotFiredWithoutTargets(t *testing.T) {
	recorder := &captureOnMerge{}
	// Dead ratio never trips (2.0) and count target is huge: nothing is eligible.
	e := callbackMergeEngine(t, recorder.fn(), 2.0, 1<<30)
	defer e.Close()

	if err := e.Add([]Document{doc("a", "x"), doc("b", "x")}); err != nil {
		t.Fatal(err)
	}
	// Nudge the merger and let several ticks pass; no eligible target → no merge.
	e.signalMerge()
	waitForNoMerge(e)

	if got := len(recorder.snapshot()); got != 0 {
		t.Fatalf("OnMerge fired %d times with no merge target, want 0", got)
	}
	if mc := e.MergeCount(); mc != 0 {
		t.Fatalf("MergeCount = %d with no eligible target, want 0", mc)
	}
}

// TestNilOnMergeRunsMergeWithoutPanic asserts an engine constructed with a nil
// OnMerge (the default for every existing caller) performs a real merge with zero
// callback invocations and no panic.
func TestNilOnMergeRunsMergeWithoutPanic(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  0.33,
		SegmentCountTarget: 1 << 30,
		// OnMerge deliberately unset (nil).
	}))
	defer e.Close()

	if err := e.Add([]Document{
		doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x"),
	}); err != nil {
		t.Fatal(err)
	}
	e.Delete("a")
	e.Delete("b")

	waitForMerge(t, e)
	// A nil callback must leave the corpus correct: only live docs survive.
	got := searchIDs(e.Search(mockQuery{term: "x"}, 10))
	if len(got) != 2 {
		t.Fatalf("post-merge search has %d ids, want 2 (c,d)", len(got))
	}
}

// pollResults waits until at least want results are recorded (or a short deadline)
// and returns the snapshot. The callback fires immediately after mergeCnt.Add on
// the same goroutine, so this bounds the tiny observe-after-count window.
func pollResults(c *captureOnMerge, want int) []MergeResult {
	step := mergeWaitTimeout / 200
	for range 200 {
		if got := c.snapshot(); len(got) >= want {
			return got
		}
		time.Sleep(step)
	}
	return c.snapshot()
}

// waitForNoMerge lets several merge ticks elapse so a "no merge expected"
// assertion is not racing the first tick. It deliberately does not assert.
func waitForNoMerge(e *SegmentedIndex[mockQuery, mockStats]) {
	// 5 tick intervals is ample for the merger to evaluate (and decline) the
	// trigger several times.
	time.Sleep(5 * mergeTickInterval)
	_ = e
}
