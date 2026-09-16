// SPDX-License-Identifier: Apache-2.0

// payload_provenance_test.go covers WHERE a resident payload's bytes live: every
// site that publishes a BUILT payload records what that payload retains, and an
// imported one takes its provenance from the blob it arrived in. What the mapping
// swap must hold while it releases those bytes is remap_swap_test.go.
//
// THE MATRIX IS ONE CELL PER PUBLISHING SITE, because the four Build publishers
// reach newEntry by four different routes (a seal, a single-partition swap, a
// group harvest's fresh segment, a layer build) and a site that forgot the
// provenance would be invisible from any of the others.

package searchengine

import (
	"strings"
	"testing"
)

// encodedLen is the size of a published entry's own encoded bytes — the quantity a
// heap-backed payload retains, read from the payload rather than modeled.
func encodedLen(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], id SegmentID) int64 {
	t.Helper()
	entry := e.set.Load().entryByID(id)
	if entry == nil {
		t.Fatalf("segment %s is not resident", id)
	}
	blob, err := entry.payload.Encode()
	if err != nil {
		t.Fatalf("encoding segment %s: %v", id, err)
	}
	return int64(len(blob))
}

// heapPayloadOf reads the entry's recorded heap-backed blob size.
func heapPayloadOf(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], id SegmentID) int64 {
	t.Helper()
	entry := e.set.Load().entryByID(id)
	if entry == nil {
		t.Fatalf("segment %s is not resident", id)
	}
	return entry.heapPayload
}

// TestSealedPayloadRecordsItsRetainedHeap is the seal cell of the publisher
// matrix, over the input classes the seal actually meets.
func TestSealedPayloadRecordsItsRetainedHeap(t *testing.T) {
	cases := []struct {
		name string
		docs []Document
	}{
		{name: "one document", docs: []Document{doc("a", "alpha")}},
		{name: "a batch", docs: []Document{doc("a", "alpha"), doc("b", "beta"), doc("c", "gamma")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, 1)
			if err := e.Add(tc.docs); err != nil {
				t.Fatalf("add: %v", err)
			}
			blobs := e.Export()
			if len(blobs) != 1 {
				t.Fatalf("seal published %d segments, want 1", len(blobs))
			}
			want := encodedLen(t, e, blobs[0].ID)
			if got := heapPayloadOf(t, e, blobs[0].ID); got != want {
				t.Errorf("sealed entry records %d heap-backed payload bytes, want its encoded length %d", got, want)
			}
			if want <= 0 {
				t.Fatalf("fixture control: the encoded segment must be non-empty, got %d bytes", want)
			}
		})
	}
}

// TestEmptyBatchSealsNothing is the seal cell's empty input class: it publishes no
// segment at all, so there is no unaccounted payload hiding behind a zero.
func TestEmptyBatchSealsNothing(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add(nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := e.ResidentSegmentCount(); got != 0 {
		t.Errorf("an empty batch published %d segments, want 0", got)
	}
	if got := e.ResidentHeapBytes(); got != 0 {
		t.Errorf("an empty engine models %d resident heap bytes, want 0", got)
	}
}

// TestBuildLayerPayloadsAreHeapBacked is the BuildLayer cell. It is written
// separately rather than folded into the seal cell because a built layer's entries
// leave the engine by their own route (BuiltLayer exports their bytes before the
// swap publishes them), which is exactly the route a provenance set only on the
// seal path would miss.
func TestBuildLayerPayloadsAreHeapBacked(t *testing.T) {
	e := newTestEngine(t, 1<<20) // never auto-seal: the layer is the only publisher

	built, err := e.BuildLayer([]BucketWork{
		{Bucket: 0, Docs: []Document{doc("a", "alpha")}},
		{Bucket: 1, Docs: []Document{doc("b", "beta")}},
		{Bucket: 2}, // the empty partition class: contributes no segment
	})
	if err != nil {
		t.Fatalf("build layer: %v", err)
	}
	if built.Len() != 2 {
		t.Fatalf("layer holds %d partitions, want 2", built.Len())
	}
	for i, entry := range built.entries {
		blob, err := entry.payload.Encode()
		if err != nil {
			t.Fatalf("encoding partition %d: %v", i, err)
		}
		if entry.heapPayload != int64(len(blob)) {
			t.Errorf("layer partition %d records %d heap-backed bytes, want its encoded length %d",
				i, entry.heapPayload, len(blob))
		}
	}

	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("replace layer: %v", err)
	}
	// And the published layer is accounted: the model must see the blobs the
	// rebuild's own partitions are holding.
	var wantBlobs int64
	for _, b := range e.Export() {
		wantBlobs += int64(len(b.Bytes))
	}
	if wantBlobs <= 0 {
		t.Fatalf("fixture control: the published layer must carry bytes, got %d", wantBlobs)
	}
	if got := e.ResidentHeapBytes(); got <= wantBlobs {
		t.Errorf("resident heap %d does not even cover the layer's %d retained blob bytes", got, wantBlobs)
	}
}

// TestSingleBucketSwapFreshPayloadIsHeapBacked is the ReplaceBucket cell. The
// fresh segment it builds is a merge INPUT rather than a published entry, so the
// observable is the swap's published output plus the fact that the merged result
// — which is mapping-backed — records no heap payload at all.
func TestSingleBucketSwapFreshPayloadIsHeapBacked(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	resident := e.Export()
	if len(resident) != 1 {
		t.Fatalf("expected one resident segment, got %d", len(resident))
	}

	published, err := e.ReplaceBucket(0, 1, []SegmentID{resident[0].ID}, nil, []Document{doc("b", "beta")})
	if err != nil {
		t.Fatalf("replace bucket: %v", err)
	}
	if published == "" {
		t.Fatal("the swap published nothing")
	}
	// THE MERGED OUTPUT IS MAPPED, and this is the cell that proves the merge path
	// was not swept into payloadBuilt by the provenance change.
	if got := heapPayloadOf(t, e, published); got != 0 {
		t.Errorf("the merged output records %d heap-backed payload bytes, want 0 — its payload is a mapping", got)
	}
	// KNOWN-POSITIVE: both documents survived the swap, so the zero above is a
	// mapped payload and not an empty one.
	for _, term := range []string{"alpha", "beta"} {
		if got := searchIDs(e.Search(mockQuery{term: term}, 10)); len(got) != 1 {
			t.Errorf("term %q should match exactly one document after the swap, got %v", term, got)
		}
	}
}

// TestImportedBlobProvenanceFollowsTheBlob covers entryFromDecoded's rule, whose
// three arms are the three ways a blob can arrive and whose middle arm is the one a
// careless model gets wrong.
//
// A blob that OWNS a mapping (Release set) or BORROWS memory another entry owns
// (keepAlive set) contributes no heap of its own; a blob that is a plain slice —
// what a pull over the wire is — is retained by the payload for its whole life and
// must be counted, or a pool loaded that way is invisible to the budget.
func TestImportedBlobProvenanceFollowsTheBlob(t *testing.T) {
	src := residencyFixture(t, strings.Repeat("x", 20_000))
	exported := src.Export()
	if len(exported) != 2 {
		t.Fatalf("fixture control: expected two exported segments, got %d", len(exported))
	}

	cases := []struct {
		name       string
		blob       func(b SegmentBlob) SegmentBlob
		wantCounts bool
	}{
		{
			name:       "a mapping the entry owns is page cache",
			blob:       func(b SegmentBlob) SegmentBlob { return SegmentBlob{ID: b.ID, Bytes: b.Bytes, Release: func() {}} },
			wantCounts: false,
		},
		{
			name:       "a borrowed view is accounted by its owner",
			blob:       func(b SegmentBlob) SegmentBlob { return b }, // Export sets keepAlive
			wantCounts: false,
		},
		{
			name:       "a plain heap slice is the entry's own heap",
			blob:       func(b SegmentBlob) SegmentBlob { return SegmentBlob{ID: b.ID, Bytes: b.Bytes} },
			wantCounts: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1 << 20}))
			if err := dst.Import([]SegmentBlob{tc.blob(exported[0])}, nil); err != nil {
				t.Fatalf("import: %v", err)
			}
			entry := dst.set.Load().entryByID(exported[0].ID)
			if entry == nil {
				t.Fatal("the imported segment is not resident")
			}
			blobLen := int64(len(exported[0].Bytes))
			if blobLen <= 0 {
				t.Fatal("fixture control: the exported blob must carry bytes")
			}
			switch {
			case tc.wantCounts && entry.heapPayload != blobLen:
				t.Errorf("a plain heap blob records %d heap bytes, want its length %d", entry.heapPayload, blobLen)
			case !tc.wantCounts && entry.heapPayload != 0:
				t.Errorf("a page-cache-backed or borrowed blob records %d heap bytes, want 0", entry.heapPayload)
			}
		})
	}
}
