// SPDX-License-Identifier: Apache-2.0

// remap_cleanup_test.go covers who OWNS the mapping a swap publishes: the release
// rides the winning entry's reachability and is never called at the swap's return,
// including when a second swap lands while a reader still holds the first's
// snapshot.
//
// It is separate from remap_swap_test.go, which covers what the swap must not move
// (results, route, statistics), because these rows assert on the RELEASE rather
// than on anything a search can see — a format double whose Decode copies its
// input keeps answering correctly after an early unmap, which is how this property
// came to be pinned by nothing.

package searchengine

import (
	"runtime"
	"sync/atomic"
	"testing"
)

// TestRemapAttachesCleanupOnlyOnTheWinner is the seal path's use-after-unmap row,
// and it is the one the merge path's tests do not reach.
//
// THE PROPERTY: the mapping handed to a swap is owned by the entry the swap
// publishes, so its release must ride that entry's reachability and must NOT be
// called when the swap returns. A reader that loaded the post-swap snapshot is
// inside those bytes; freeing them at the return unmaps memory a live reader is
// walking, and the failure surfaces as a fault at whatever later moment the
// address range is reused rather than at the call that caused it.
//
// IT IS WRITTEN AGAINST THE RELEASE ITSELF rather than against a search result,
// because a format double whose Decode COPIES its input — this package's mock does
// — keeps answering correctly after an early unmap, which is exactly how this
// property came to be pinned by nothing.
func TestRemapAttachesCleanupOnlyOnTheWinner(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	if len(blobs) != 1 {
		t.Fatalf("expected one segment, got %d", len(blobs))
	}

	var released atomic.Int64
	if err := e.RemapResident(blobs[0].ID, SegmentBlob{
		ID: blobs[0].ID, Bytes: blobs[0].Bytes, Release: func() { released.Add(1) },
	}); err != nil {
		t.Fatalf("remap: %v", err)
	}

	runtime.GC()
	runtime.GC()
	if got := released.Load(); got != 0 {
		t.Errorf("the mapping was released %d times while its entry is still resident — "+
			"a live reader of the published snapshot is inside those bytes", got)
	}
	if got := searchIDs(e.Search(mockQuery{term: "alpha"}, 10)); len(got) != 1 {
		t.Errorf("the remapped segment stopped answering: %v", got)
	}

	// CONTROL: the cleanup DOES run once the entry leaves the set, so the zero above
	// is "not yet" rather than "never attached" — an unattached mapping would leak
	// with no error and no test failure.
	e.Unload([]SegmentID{blobs[0].ID})
	if !waitForRelease(&released) {
		t.Error("the mapping was never released after its entry left the set — the cleanup was not attached at all")
	}
}

// TestSecondSwapKeepsTheFirstMappingUnderAHeldSnapshot is the adversary: two swaps
// of one segment, with a reader holding the snapshot the FIRST swap published.
//
// The second swap makes the first entry garbage in the published set, but a reader
// that loaded that set before the swap is still walking its entries — so the first
// mapping must stay mapped until that snapshot is unreachable, and the second must
// not be released at all while it is the published one.
func TestSecondSwapKeepsTheFirstMappingUnderAHeldSnapshot(t *testing.T) {
	e := newTestEngine(t, 1)
	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	id, bytes := blobs[0].ID, blobs[0].Bytes

	var first, second atomic.Int64
	if err := e.RemapResident(id, SegmentBlob{ID: id, Bytes: bytes, Release: func() { first.Add(1) }}); err != nil {
		t.Fatalf("first remap: %v", err)
	}
	held := e.set.Load() // a reader's snapshot, taken between the two swaps
	if err := e.RemapResident(id, SegmentBlob{ID: id, Bytes: bytes, Release: func() { second.Add(1) }}); err != nil {
		t.Fatalf("second remap: %v", err)
	}

	runtime.GC()
	runtime.GC()
	if got := first.Load(); got != 0 {
		t.Errorf("the first mapping was released %d times while a reader still held its snapshot", got)
	}
	// The held snapshot still answers out of the entry the first swap published.
	if entry := held.entryByID(id); entry == nil {
		t.Fatal("the held snapshot lost its entry")
	}
	runtime.KeepAlive(held)

	// Drop the reader's snapshot: now the first entry is unreachable and its mapping
	// may be freed. The SECOND mapping is the published one and must not be.
	held = nil
	if !waitForRelease(&first) {
		t.Error("the first mapping was never released after its snapshot was dropped")
	}
	if got := second.Load(); got != 0 {
		t.Errorf("the published mapping was released %d times", got)
	}
	if got := searchIDs(e.Search(mockQuery{term: "alpha"}, 10)); len(got) != 1 {
		t.Errorf("the segment stopped answering after the second swap: %v", got)
	}
}
