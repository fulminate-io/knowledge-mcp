// SPDX-License-Identifier: Apache-2.0

// remap_swap_test.go covers what the mapping swap must hold while it happens: a
// search in flight sees one snapshot, the route is carried rather than rebuilt,
// the format's statistics are refolded rather than carried, and a group harvest
// holds no more built payloads at once than it has workers.
//
// It is the sibling of payload_provenance_test.go, which covers WHERE a payload's
// bytes live; this file covers what changing that costs and what it must not move.

package searchengine

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// TestGroupHarvestHoldsAtMostOneBuiltPayloadPerWorker is requirement 3's bound. It
// is a BOUND rather than a release: `fresh` is a local of harvestPartition and only
// its id escapes, so the simultaneous ceiling is the WORKER count — min(NumCPU,
// len(work)) — and there is nothing to release earlier than the harvest's return.
//
// The instrument counts live BUILT payloads: buildCountingFormat increments when
// Build hands one out and decrements when that partition's MergeTo has consumed it,
// recording the peak.
func TestGroupHarvestHoldsAtMostOneBuiltPayloadPerWorker(t *testing.T) {
	workers := runtime.NumCPU()
	cases := []struct {
		name       string
		partitions int
		withDocs   bool
		wantBuilds int
	}{
		{name: "every partition builds a fresh segment", partitions: 4, withDocs: true, wantBuilds: 4},
		{name: "no partition carries documents", partitions: 4, withDocs: false, wantBuilds: 0},
		{name: "more partitions than workers", partitions: 4 * workers, withDocs: true, wantBuilds: 4 * workers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &buildCountingFormat{}
			e := closeOnCleanup(t, New[mockQuery, mockStats](f, Options{
				MinSegmentDocs: 1 << 20, ScratchDir: t.TempDir(),
			}))
			// One resident constituent spanning every partition, so each harvest has
			// real work whether or not it also builds a fresh segment.
			seed := make([]Document, 0, tc.partitions)
			for i := range tc.partitions {
				seed = append(seed, doc(bucketedID("seed", i), "alpha"))
			}
			if err := e.Add(seed); err != nil {
				t.Fatalf("seed add: %v", err)
			}
			if err := e.Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}
			resident := e.Export()
			if len(resident) != 1 {
				t.Fatalf("seed published %d segments, want 1", len(resident))
			}
			constituents := []SegmentID{resident[0].ID}

			work := make([]BucketWork, 0, tc.partitions)
			for i := range tc.partitions {
				w := BucketWork{Bucket: i}
				if tc.withDocs {
					w.Docs = []Document{doc(bucketedID("fresh", i), "beta")}
				}
				work = append(work, w)
			}

			f.reset()
			if _, _, err := e.ReplaceBucketGroup(context.Background(), tc.partitions, constituents, work); err != nil {
				t.Fatalf("group swap: %v", err)
			}

			bound := min(workers, tc.partitions)
			if got := f.peak(); got > int64(bound) {
				t.Errorf("the harvest held %d built payloads at once, above the min(NumCPU=%d, partitions=%d)=%d bound",
					got, workers, tc.partitions, bound)
			}
			if got := f.builds(); got != int64(tc.wantBuilds) {
				t.Errorf("the harvest built %d fresh payloads, want %d — the instrument counted the wrong thing",
					got, tc.wantBuilds)
			}
		})
	}
}

// bucketedID names a document so the partitioner spreads the fixture across
// partitions rather than piling it into one.
func bucketedID(prefix string, i int) string {
	return prefix + "-" + strconv.Itoa(i)
}

// buildCountingFormat counts BUILT payloads that are simultaneously live: Build
// hands one out, and that partition's MergeTo is the last thing that reads it
// before harvestPartition returns and drops it.
//
// IT EMBEDS mockFormat AND DELEGATES rather than reimplementing, and its MergeTo
// calls mockFormat's so the double's own gate runs around the real merge instead
// of replacing it.
type buildCountingFormat struct {
	mockFormat
	live    atomic.Int64
	maxLive atomic.Int64
	built   atomic.Int64
}

func (f *buildCountingFormat) reset() {
	f.live.Store(0)
	f.maxLive.Store(0)
	f.built.Store(0)
}

func (f *buildCountingFormat) peak() int64   { return f.maxLive.Load() }
func (f *buildCountingFormat) builds() int64 { return f.built.Load() }

func (f *buildCountingFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	seg, rep, err := f.mockFormat.Build(docs)
	if err != nil {
		return seg, rep, err
	}
	f.built.Add(1)
	now := f.live.Add(1)
	for {
		peak := f.maxLive.Load()
		if now <= peak || f.maxLive.CompareAndSwap(peak, now) {
			break
		}
	}
	return seg, rep, err
}

func (f *buildCountingFormat) MergeTo(
	dst MergeSink, segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool,
) (int64, error) {
	n, err := f.mockFormat.MergeTo(dst, segs, accept)
	// The fresh payload is a merge input and dies when this partition's harvest
	// returns; the merge is its last reader, so the count falls here.
	if f.live.Load() > 0 {
		f.live.Add(-1)
	}
	return n, err
}

// TestSearchAcrossTheRemapSwapSeesOneSnapshot is requirement 1's concurrency row
// for the SEAL path's release, which the merge path's remap tests never reach.
//
// THE PROPERTY IS ATOMIC PER SET, NOT PER ENTRY: RemapResident copies the whole
// entry slice and CASes the set pointer, so a reader holding the pre-swap snapshot
// keeps reading the old heap-backed payload — whose bytes stay reachable from that
// snapshot — and the cleanup that could free anything is attached only after the
// CAS wins.
//
// THE CONTROL IS THAT THE SWAP LANDED INSIDE THE WINDOW. Without it this test
// passes on a run where no remap happened at all: the provenance is read before and
// after, and the entry must have gone from heap-backed to mapping-backed while the
// searchers were running.
func TestSearchAcrossTheRemapSwapSeesOneSnapshot(t *testing.T) {
	e := newTestEngine(t, 1)

	docs := []Document{doc("a", "alpha"), doc("b", "alpha"), doc("c", "alpha")}
	if err := e.Add(docs); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	if len(blobs) != 1 {
		t.Fatalf("expected one segment, got %d", len(blobs))
	}
	id := blobs[0].ID
	before := heapPayloadOf(t, e, id)
	if before <= 0 {
		t.Fatalf("control: the sealed entry must be heap-backed before the swap, got %d", before)
	}

	want := searchIDs(e.Search(mockQuery{term: "alpha"}, 10))
	if len(want) != len(docs) {
		t.Fatalf("control: the fixture must match every document, got %v", want)
	}

	var searches atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := searchIDs(e.Search(mockQuery{term: "alpha"}, 10))
				if len(got) != len(want) {
					t.Errorf("a search across the swap saw %v, want %v", got, want)
					return
				}
				for i := range got {
					if got[i] != want[i] {
						t.Errorf("a search across the swap saw %v, want %v", got, want)
						return
					}
				}
				searches.Add(1)
			}
		})
	}

	// The same bytes the entry was published from — which is what the production
	// path hands in, since a segment id is its payload's content hash.
	if err := e.RemapResident(id, SegmentBlob{ID: id, Bytes: blobs[0].Bytes}); err != nil {
		t.Fatalf("remap: %v", err)
	}
	// Let the searchers run past the swap before stopping them.
	for searches.Load() < 100 {
		runtime.Gosched()
	}
	close(stop)
	wg.Wait()

	if got := heapPayloadOf(t, e, id); got != 0 {
		t.Errorf("after the swap the entry still records %d heap-backed bytes, want 0", got)
	}
	if searches.Load() < 100 {
		t.Errorf("control: only %d searches ran, so the window may not have contained the swap", searches.Load())
	}
}

// TestRemapCarriesTheRouteInsteadOfRebuildingIt pins the swap's COST SHAPE, which
// is what makes releasing a whole pool's sealed segments affordable at all.
//
// A remap replaces one payload with byte-identical bytes under the same members and
// the same id, so the route, the distinct count and the corpus statistics are
// unchanged by construction. Rebuilding them would re-flatten the whole route per
// remapped segment — quadratic over a pool — and would also move an appended entry
// out of the tail, making the swap observable in a structure it must not touch.
func TestRemapCarriesTheRouteInsteadOfRebuildingIt(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := e.Add([]Document{doc("b", "beta")}); err != nil {
		t.Fatalf("add: %v", err)
	}

	before := e.set.Load()
	if len(before.base) != 0 || before.baseN != 0 {
		t.Fatalf("fixture control: two appends must still route out of the tail, got base=%d baseN=%d",
			len(before.base), before.baseN)
	}
	blobs := e.Export()
	if len(blobs) != 2 {
		t.Fatalf("expected two segments, got %d", len(blobs))
	}

	if err := e.RemapResident(blobs[0].ID, SegmentBlob{ID: blobs[0].ID, Bytes: blobs[0].Bytes}); err != nil {
		t.Fatalf("remap: %v", err)
	}

	after := e.set.Load()
	if len(after.base) != len(before.base) || after.baseN != before.baseN {
		t.Errorf("the remap changed the route's shape: base %d→%d, baseN %d→%d — it re-flattened a set it only swapped a payload in",
			len(before.base), len(after.base), before.baseN, after.baseN)
	}
	if after.distinct != before.distinct {
		t.Errorf("the remap changed the distinct count %d→%d", before.distinct, after.distinct)
	}
	if after.stats.totalDocs != before.stats.totalDocs {
		t.Errorf("the remap changed the corpus statistics %d→%d over byte-identical bytes",
			before.stats.totalDocs, after.stats.totalDocs)
	}
	// KNOWN-POSITIVE: routing still answers for both segments after the swap, so the
	// carried route is a working route and not a stale one.
	for _, term := range []string{"alpha", "beta"} {
		if got := searchIDs(e.Search(mockQuery{term: term}, 10)); len(got) != 1 {
			t.Errorf("term %q should still match exactly one document after the remap, got %v", term, got)
		}
	}
	if got := e.set.Load().entryOf("a"); got == nil {
		t.Error("the carried route no longer resolves a member of the remapped segment")
	}
}

// TestRemapRefoldsTheStatsOverTheLivePayloads is the regression catcher for a
// use-after-unmap this change first produced and then fixed, and it is the reason
// the snapshot's statistics are re-folded while its route is carried.
//
// A FORMAT'S STATS RETAIN THEIR SEGMENTS — bm25's CorpusStats answers document
// frequency by probing the payloads it was folded over, and the mock does the same
// so this class is visible here. Carry the old stats object across a payload swap
// and the new snapshot keeps probing the OLD payload, whose entry is no longer in
// any set: that entry is collected, its cleanup unmaps its blob, and the next
// document-frequency question reads unmapped memory. Observed as a
// slice-bounds-out-of-range panic deep inside a search, at whatever later moment
// the address range was reused.
//
// The assertion is the invariant rather than the symptom: every payload a
// snapshot's stats can probe must be a payload of that snapshot's own entries.
func TestRemapRefoldsTheStatsOverTheLivePayloads(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := e.Add([]Document{doc("b", "beta")}); err != nil {
		t.Fatalf("add: %v", err)
	}

	blobs := e.Export()
	if len(blobs) != 2 {
		t.Fatalf("expected two segments, got %d", len(blobs))
	}
	stale := e.set.Load().entryByID(blobs[0].ID).payload
	if got := e.set.Load().stats.probedSegments(); len(got) != 2 {
		t.Fatalf("fixture control: the stats must retain both payloads before the swap, got %d", len(got))
	}

	if err := e.RemapResident(blobs[0].ID, SegmentBlob{ID: blobs[0].ID, Bytes: blobs[0].Bytes}); err != nil {
		t.Fatalf("remap: %v", err)
	}

	set := e.set.Load()
	live := make(map[Segment[mockQuery, mockStats]]bool, len(set.entries))
	for _, entry := range set.entries {
		live[entry.payload] = true
	}
	probed := set.stats.probedSegments()
	if len(probed) != len(set.entries) {
		t.Errorf("the snapshot's stats probe %d payloads for %d entries", len(probed), len(set.entries))
	}
	for _, seg := range probed {
		if !live[seg] {
			t.Error("the snapshot's stats probe a payload that is not in the snapshot — that payload's entry is free to be collected and its mapping unmapped underneath the probe")
		}
		if seg == stale {
			t.Error("the stats still probe the payload the remap replaced")
		}
	}
	// KNOWN-POSITIVE: the refolded stats still describe the same corpus.
	if got := set.stats.totalDocs; got != 2 {
		t.Errorf("the refolded stats report %d documents, want 2", got)
	}
}

// TestRemapBatchDistinguishesDeclinedFromRepublished pins the three-way outcome.
//
// A DECLINE AND A REPUBLICATION USED TO SHARE A nil ERROR, and a caller that
// counted "not an error" as "swapped" could print an operator-facing release count
// for a batch in which nothing was swapped at all — every id having left the set
// between the caller's list and the CAS. The counting is the reason the seam
// reports anything, so the two are separate values with separate meanings, and a
// failure carries its error beside its outcome.
func TestRemapBatchDistinguishesDeclinedFromRepublished(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := e.Add([]Document{doc("b", "beta")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	if len(blobs) != 2 {
		t.Fatalf("expected two segments, got %d", len(blobs))
	}
	resident, gone := blobs[0], blobs[1]

	// The id that left the set between the caller's list and this call.
	e.Unload([]SegmentID{gone.ID})

	var residentReleased, goneReleased, badReleased atomic.Int64
	bad := SegmentBlob{ID: "not-a-segment", Bytes: []byte("{"), Release: func() { badReleased.Add(1) }}

	got := e.RemapResidentBatch([]SegmentBlob{
		{ID: resident.ID, Bytes: resident.Bytes, Release: func() { residentReleased.Add(1) }},
		{ID: gone.ID, Bytes: gone.Bytes, Release: func() { goneReleased.Add(1) }},
		bad,
	})

	if got[resident.ID].Outcome != RemapRepublished {
		t.Errorf("a resident segment's outcome is %v, want RemapRepublished", got[resident.ID].Outcome)
	}
	if got[resident.ID].Err != nil {
		t.Errorf("a republished segment carries an error: %v", got[resident.ID].Err)
	}
	if got[gone.ID].Outcome != RemapDeclined {
		t.Errorf("a segment that left the set has outcome %v, want RemapDeclined", got[gone.ID].Outcome)
	}
	if got[gone.ID].Err != nil {
		t.Errorf("a decline is not an error, got %v", got[gone.ID].Err)
	}
	if got[bad.ID].Outcome != RemapFailed {
		t.Errorf("an undecodable blob has outcome %v, want RemapFailed", got[bad.ID].Outcome)
	}
	if got[bad.ID].Err == nil {
		t.Error("an undecodable blob must carry its error beside its outcome")
	}

	// OWNERSHIP IS UNCHANGED BY THE SPLIT: the winner's release rides its new
	// entry's cleanup (so it has NOT run yet), and both non-winners are freed here
	// rather than stranded.
	if residentReleased.Load() != 0 {
		t.Error("a republished blob's release must be attached to the new entry, not called")
	}
	if goneReleased.Load() != 1 {
		t.Errorf("a declined blob's mapping must be released exactly once, got %d", goneReleased.Load())
	}
	if badReleased.Load() != 1 {
		t.Errorf("an undecodable blob's mapping must be released exactly once, got %d", badReleased.Load())
	}
}
