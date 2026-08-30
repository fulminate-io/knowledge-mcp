package searchengine

import (
	"fmt"
	"sync"
	"testing"
)

// bucket_group_test.go gates ReplaceBucketGroup's contract at its edges: the swap
// is atomic across the whole group, a failure anywhere publishes nothing, and the
// reclaim event spares every id the group published rather than only one.

// gateFormat wraps mockFormat so a test can BLOCK or FAIL a chosen Merge call.
// Blocking is what makes the atomicity observation deterministic: a reader that
// merely spins hoping to catch an intermediate state is probabilistic in the
// direction that HIDES an N-CAS implementation, whose window is microseconds.
type gateFormat struct {
	mockFormat

	mu      sync.Mutex
	calls   int
	blockAt int           // 1-based Merge call to hold; 0 disables
	entered chan struct{} // closed when the held call is reached
	release chan struct{} // closed by the test to let it proceed
	failAt  int           // 1-based Merge call to fail; 0 disables
}

// MergeTo MUST BE DECLARED HERE. gateFormat embeds mockFormat, so without this
// declaration it would silently PROMOTE mockFormat.MergeTo — whose receiver is
// the embedded value, so the call counter never advances and neither the blockAt
// hold nor the failAt injection fires. The engine calls MergeTo, so every gate
// this double exists to impose would evaporate while every test using it still
// COMPILED and, worse, still passed:
// TestGroupSwapPublishesNothingOnPartialFailure would see call 2 succeed and the
// group publish. That is the failure this declaration prevents, and it is the
// reason the promotion is called out here rather than left to be noticed.
func (g *gateFormat) MergeTo(dst MergeSink, segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool) (int64, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()

	if g.failAt == n {
		return 0, fmt.Errorf("injected merge failure on call %d", n)
	}
	if g.blockAt == n {
		close(g.entered)
		<-g.release
	}
	return g.mockFormat.MergeTo(dst, segs, accept)
}

// groupIDsFor returns n ids that hash into the given partition under count 2.
func groupIDsFor(t *testing.T, bucket, n int) []string {
	t.Helper()
	var out []string
	for i := 0; len(out) < n; i++ {
		id := fmt.Sprintf("g-%05d", i)
		if BucketOf(id, 2) == bucket {
			out = append(out, id)
		}
		if i > 100000 {
			t.Fatalf("could not find %d ids for bucket %d", n, bucket)
		}
	}
	return out
}

// sealSegment adds docs and force-seals them into their own segment, returning it.
func sealSegment(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], ids []string, content string) SegmentID {
	t.Helper()
	before := map[SegmentID]bool{}
	for _, b := range e.Export() {
		before[b.ID] = true
	}
	docs := make([]Document, 0, len(ids))
	for _, id := range ids {
		docs = append(docs, doc(id, content+" "+id))
	}
	if err := e.Add(docs); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, b := range e.Export() {
		if !before[b.ID] {
			return b.ID
		}
	}
	t.Fatal("no new segment sealed")
	return ""
}

// residentSnapshot returns the resident segment ids as a set, for byte-identical
// comparison across a failed operation.
func residentSnapshot(e *SegmentedIndex[mockQuery, mockStats]) map[SegmentID]int {
	out := map[SegmentID]int{}
	for _, b := range e.Export() {
		out[b.ID] = b.DocCount
	}
	return out
}

// TestGroupSwapIsAtomicAcrossPartitions pins that a reader concurrent with a group
// swap sees ALL-OLD or ALL-NEW, never a hole.
//
// AN N-SEPARATE-CAS IMPLEMENTATION FAILS THIS AND PASSES BOTH INVESTIGATION GATES,
// which is why the gate exists: with one partition's Merge held, an N-CAS
// implementation has necessarily already published its other partitions and
// removed their shared constituents, so the read below finds those constituents
// gone and their members in no segment. The group form publishes nothing until
// every build completes, so the same read finds the original set untouched.
func TestGroupSwapIsAtomicAcrossPartitions(t *testing.T) {
	fmtGate := &gateFormat{blockAt: 2, entered: make(chan struct{}), release: make(chan struct{})}
	e := closeOnCleanup(t, New[mockQuery, mockStats](fmtGate, Options{
		MinSegmentDocs:     1 << 20, // only explicit Flush seals
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	defer e.Close()

	// A segment SPANNING both partitions is what makes the group a group.
	spanning := append(groupIDsFor(t, 0, 8), groupIDsFor(t, 1, 8)...)
	shared := sealSegment(t, e, spanning, "alpha")
	before := residentSnapshot(e)

	done := make(chan error, 1)
	go func() {
		_, _, err := e.ReplaceBucketGroup(2, []SegmentID{shared}, []BucketWork{
			{Bucket: 0}, {Bucket: 1},
		})
		done <- err
	}()

	<-fmtGate.entered
	// One partition's merge is HELD here while the other has been admitted. WHICH
	// partition is which is timing-dependent now that the harvest runs on a bounded
	// pool — both start together and gateFormat's mutex decides which call is
	// numbered 2 — and that is deliberately not asserted. The property under test is
	// unaffected either way: nothing the group produces may be visible until its
	// single CAS, so the read below must still find the original set.
	held := residentSnapshot(e)
	if len(held) != len(before) {
		t.Fatalf("mid-group read saw %d segments, want the original %d — a partition published before the group completed", len(held), len(before))
	}
	for id := range before {
		if _, ok := held[id]; !ok {
			t.Fatalf("mid-group read lost constituent %s — its members are in no segment: a HOLE", id[:12])
		}
	}

	close(fmtGate.release)
	if err := <-done; err != nil {
		t.Fatalf("ReplaceBucketGroup: %v", err)
	}

	after := residentSnapshot(e)
	if _, stillThere := after[shared]; stillThere {
		t.Fatal("the shared constituent survived the completed group swap")
	}
	if len(after) != 2 {
		t.Fatalf("after the group swap: %d segments, want one per partition (2)", len(after))
	}
}

// TestGroupSwapPublishesNothingOnPartialFailure pins the all-or-nothing contract.
//
// PUBLISHING THE PARTITIONS THAT SUCCEEDED IS THE ORIGINAL DEFECT MADE
// DETERMINISTIC: the removal set covers every constituent, so the failed
// partition's members would be discarded with a segment nobody rebuilt them from.
func TestGroupSwapPublishesNothingOnPartialFailure(t *testing.T) {
	fmtGate := &gateFormat{failAt: 2}
	e := closeOnCleanup(t, New[mockQuery, mockStats](fmtGate, Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	defer e.Close()

	spanning := append(groupIDsFor(t, 0, 8), groupIDsFor(t, 1, 8)...)
	shared := sealSegment(t, e, spanning, "alpha")
	before := residentSnapshot(e)

	if _, _, err := e.ReplaceBucketGroup(2, []SegmentID{shared}, []BucketWork{
		{Bucket: 0}, {Bucket: 1},
	}); err == nil {
		t.Fatal("a failed partition build must return an error, not a partial publish")
	}

	after := residentSnapshot(e)
	if len(after) != len(before) {
		t.Fatalf("resident set changed after a failed group: %d segments, want %d", len(after), len(before))
	}
	for id, docCount := range before {
		got, ok := after[id]
		if !ok {
			t.Fatalf("segment %s was removed by a group that published nothing", id[:12])
		}
		if got != docCount {
			t.Fatalf("segment %s DocCount changed %d -> %d", id[:12], docCount, got)
		}
	}
}

// TestGroupReclaimSparesEveryPublishedID pins that the reclaim event excludes the
// group's WHOLE published set.
//
// THE ALIASING PRECONDITION IS ASSERTED EXPLICITLY, not assumed. A merge of a
// SINGLE segment converges to that segment's own content hash, so partition 1's
// output below deterministically carries its constituent's id. If a future change
// to Build or Merge alters the encoding so that stops being true, the collision
// never happens, the reclaim never has a chance to delete a live blob, and this
// test would pass forever while checking nothing. The precondition assert is what
// turns that silent vacuity into a failure.
func TestGroupReclaimSparesEveryPublishedID(t *testing.T) {
	var fired []MergeResult
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
		OnMerge:            func(res MergeResult) { fired = append(fired, res) },
	}))
	defer e.Close()

	// Partition 0 gets TWO constituents, so its output is a genuine consolidation
	// with a new content hash. Partition 1 gets ONE — the closure-added
	// empty-partition shape, zero incoming documents and a single constituent —
	// so its output is a merge-of-one that aliases that constituent's id.
	p0a := sealSegment(t, e, groupIDsFor(t, 0, 6), "alpha")
	p0b := sealSegment(t, e, groupIDsFor(t, 0, 12)[6:], "beta")
	p1 := sealSegment(t, e, groupIDsFor(t, 1, 6), "gamma")

	published, _, err := e.ReplaceBucketGroup(2, []SegmentID{p0a, p0b, p1}, []BucketWork{
		{Bucket: 0}, {Bucket: 1},
	})
	if err != nil {
		t.Fatalf("ReplaceBucketGroup: %v", err)
	}

	// PRECONDITION: partition 1's output must genuinely alias its consumed
	// constituent, or this test proves nothing.
	if published[1] != p1 {
		t.Fatalf("aliasing precondition NOT met: partition 1 published %s, consumed constituent was %s — the merge-of-one no longer reproduces its input's hash, so this test would be vacuous",
			published[1][:12], p1[:12])
	}
	if published[0] == p0a || published[0] == p0b {
		t.Fatalf("partition 0's output aliased a constituent (%s); the fixture wants it to be a genuine consolidation", published[0][:12])
	}

	// THE GATE: no reclaim event may name an id the group published — that blob is
	// live. A single-id excluding() spares only the firing entry's own id and hands
	// the owner partition 1's live blob to delete.
	publishedIDs := map[SegmentID]bool{}
	for _, id := range published {
		publishedIDs[id] = true
	}
	if len(fired) == 0 {
		t.Fatal("no reclaim event fired; the group consumed two segments it did not republish")
	}
	for _, res := range fired {
		for _, id := range res.Removed {
			if publishedIDs[id] {
				t.Fatalf("reclaim event names %s, which this group PUBLISHED — the owner would delete a live segment", id[:12])
			}
		}
	}

	// And the genuinely superseded constituents ARE reported, or the reclaim leaks.
	reported := map[SegmentID]bool{}
	for _, res := range fired {
		for _, id := range res.Removed {
			reported[id] = true
		}
	}
	for _, id := range []SegmentID{p0a, p0b} {
		if !reported[id] {
			t.Fatalf("consumed constituent %s was never reported for reclaim — its stored blob leaks", id[:12])
		}
	}
}
