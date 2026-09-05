// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// waitForRelease drives GC until released reports a non-zero count, or gives up.
// A cleanup runs on a background goroutine some cycles after the object becomes
// unreachable, so a single runtime.GC() is not enough to observe it.
func waitForRelease(released *atomic.Int64) bool {
	for range 50 {
		runtime.GC()
		if released.Load() > 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return released.Load() > 0
}

// TestRemapResidentPreservesInPlaceLiveness is the catcher for a
// document-resurrection defect that no other gate in this changeset sees.
//
// A merged entry is PUBLISHED AND SEARCHABLE before its payload is republished
// as a mapping, so a delete can land in the window between the two. An entry's
// liveDocs are mutated IN PLACE and without any CAS — the documented exception
// to the snapshot's immutability — so the remap must carry the SAME *liveDocs
// pointer forward. Rebuilding liveness from a tombstone slice looks lossless and
// is not: it discards every kill that landed in the window, and the document
// silently comes back from the dead in search results.
func TestRemapResidentPreservesInPlaceLiveness(t *testing.T) {
	e := newTestEngine(t, 1)
	defer e.Close()

	if err := e.Add([]Document{doc("a", "alpha"), doc("b", "beta"), doc("c", "gamma")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	if len(blobs) != 1 {
		t.Fatalf("expected one segment, got %d", len(blobs))
	}
	id := blobs[0].ID

	// The delete lands AFTER the entry is published and BEFORE the remap — the
	// exact window reclaimMerged runs in.
	e.Delete("b")
	if got := searchIDs(e.Search(mockQuery{term: "beta"}, 10)); len(got) != 0 {
		t.Fatalf("b should be dead before the remap, got %v", got)
	}

	// Remap with the SAME bytes, which is what the real path does: the blob was
	// written from exactly these bytes and a segment id is their content hash.
	if err := e.RemapResident(id, SegmentBlob{ID: id, Bytes: blobs[0].Bytes}); err != nil {
		t.Fatalf("remap: %v", err)
	}

	if got := searchIDs(e.Search(mockQuery{term: "beta"}, 10)); len(got) != 0 {
		t.Errorf("the deleted document came back after the remap: %v — liveness was rebuilt instead of carried", got)
	}
	// KNOWN-POSITIVE: the surviving documents are still searchable, so the zero
	// above is a preserved delete and not a segment that lost all its rows.
	for _, term := range []string{"alpha", "gamma"} {
		if got := searchIDs(e.Search(mockQuery{term: term}, 10)); len(got) != 1 {
			t.Errorf("term %q should still match exactly one document after the remap, got %v", term, got)
		}
	}

	// A delete arriving AFTER the remap must land too — the entry is live, not a
	// frozen copy.
	e.Delete("c")
	if got := searchIDs(e.Search(mockQuery{term: "gamma"}, 10)); len(got) != 0 {
		t.Errorf("a delete after the remap did not take effect: %v", got)
	}
}

// TestRemapResidentIgnoresAbsentSegment pins that remapping an id that is no
// longer resident is a no-op rather than a resurrection: a merge whose output was
// superseded before the reclaim hook ran must not be put back.
func TestRemapResidentIgnoresAbsentSegment(t *testing.T) {
	e := newTestEngine(t, 1)
	defer e.Close()

	if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	blobs := e.Export()
	id := blobs[0].ID
	e.Unload([]SegmentID{id})
	if err := e.RemapResident(id, SegmentBlob{ID: id, Bytes: blobs[0].Bytes}); err != nil {
		t.Fatalf("remap of an absent segment must not error: %v", err)
	}
	if got := searchIDs(e.Search(mockQuery{term: "alpha"}, 10)); len(got) != 0 {
		t.Errorf("remapping an unloaded segment resurrected it: %v", got)
	}
}

// TestRemapResidentReleasesWhenItDeclines pins the OWNERSHIP half of
// RemapResident's contract, which is the half a leak hides in.
//
// Both declining paths return a NIL error, because neither is an error: the
// segment was superseded before the remap reached it, or it vanished between a
// lost CAS and the retry. A decline that returned nil while still holding an
// unattached mapping would strand it with no error, no log and no failing test —
// the mapping would simply never be freed for the life of the process.
//
// The release is asserted SYNCHRONOUSLY here, and that is the point. The
// attached path defers to a cleanup keyed on reachability; the declined path
// must not, because nothing was ever published and no reader can hold it. The
// success case is the known-positive on the other side: it must NOT release
// synchronously, or the assertion below would pass against an implementation
// that simply released every blob it was handed.
func TestRemapResidentReleasesWhenItDeclines(t *testing.T) {
	newEngineWithSegment := func(t *testing.T) (*SegmentedIndex[mockQuery, mockStats], SegmentBlob) {
		t.Helper()
		e := newTestEngine(t, 1)
		if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
			t.Fatalf("add: %v", err)
		}
		return e, e.Export()[0]
	}

	t.Run("not resident", func(t *testing.T) {
		e, blob := newEngineWithSegment(t)
		defer e.Close()
		e.Unload([]SegmentID{blob.ID})

		var released atomic.Int64
		err := e.RemapResident(blob.ID, SegmentBlob{
			ID: blob.ID, Bytes: blob.Bytes,
			Release: func() { released.Add(1) },
		})
		if err != nil {
			t.Fatalf("declining is not an error: %v", err)
		}
		if n := released.Load(); n != 1 {
			t.Errorf("RemapResident declined and released the mapping %d times, want exactly 1 — "+
				"a decline that keeps an unattached mapping leaks it silently", n)
		}
	})

	t.Run("decode fails", func(t *testing.T) {
		e, blob := newEngineWithSegment(t)
		defer e.Close()

		var released atomic.Int64
		err := e.RemapResident(blob.ID, SegmentBlob{
			ID: blob.ID, Bytes: []byte("not a decodable segment"),
			Release: func() { released.Add(1) },
		})
		if err == nil {
			t.Fatal("an undecodable blob must error")
		}
		if n := released.Load(); n != 1 {
			t.Errorf("RemapResident errored before attaching and released %d times, want exactly 1", n)
		}
	})

	// KNOWN-POSITIVE: the successful path must NOT release synchronously —
	// ownership passed to the entry's cleanup. Without this, an implementation
	// that released unconditionally would satisfy both cases above.
	t.Run("success does not release", func(t *testing.T) {
		e, blob := newEngineWithSegment(t)
		defer e.Close()

		var released atomic.Int64
		err := e.RemapResident(blob.ID, SegmentBlob{
			ID: blob.ID, Bytes: blob.Bytes,
			Release: func() { released.Add(1) },
		})
		if err != nil {
			t.Fatalf("remap: %v", err)
		}
		if n := released.Load(); n != 0 {
			t.Errorf("RemapResident released %d times on the SUCCESS path — the mapping it just "+
				"published is still in use, and its release belongs to the entry's cleanup", n)
		}
		if got := searchIDs(e.Search(mockQuery{term: "alpha"}, 10)); len(got) != 1 {
			t.Errorf("the remapped segment stopped matching: %v", got)
		}
	})
}

// TestExportedBlobKeepsMappingAlive proves an exported blob cannot have its
// bytes unmapped underneath it.
//
// Encode on a mapped segment returns the mapping ITSELF rather than a copy —
// that identity is what makes shipping free — and Export stores those bytes in a
// blob it RETURNS. The cleanup that frees a mapping observes the ENTRY's
// reachability, and holding a byte slice does not make the entry reachable, so
// without an explicit pin the blob would be a read of released memory.
//
// The second half is the known-positive control INSIDE the test: it proves the
// cleanup machinery actually fires in this environment, so the first half's zero
// means "held", not "has not run yet".
//
// It deliberately never READS the bytes after a possible unmap. Under the broken
// shape that is a SIGBUS, and a crash is a worse gate than an assertion.
func TestExportedBlobKeepsMappingAlive(t *testing.T) {
	e := newTestEngine(t, 1)
	defer e.Close()

	var released atomic.Int64
	built, _, err := mockFormat{}.Build([]Document{doc("a", "alpha")})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	bytes, err := built.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	blob := SegmentBlob{
		ID:      "seg-mapped",
		Format:  "mock",
		Bytes:   bytes,
		Release: func() { released.Add(1) },
	}
	if err := e.Import([]SegmentBlob{blob}, nil); err != nil {
		t.Fatalf("import: %v", err)
	}

	exported := e.Export()
	if len(exported) != 1 {
		t.Fatalf("expected one exported blob, got %d", len(exported))
	}
	e.Unload([]SegmentID{exported[0].ID})

	// The entry is out of the set and nothing but the exported blob refers to
	// its payload. Without the pin the cleanup would be free to run here.
	for range 20 {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	if n := released.Load(); n != 0 {
		t.Fatalf("the mapping was released %d time(s) while an exported blob still held its bytes", n)
	}
	runtime.KeepAlive(exported)

	// KNOWN-POSITIVE: drop the last reference and the release MUST run, which is
	// what makes the zero above evidence of a pin rather than of a cleanup that
	// never fires at all.
	exported = nil
	_ = exported
	if !waitForRelease(&released) {
		t.Fatal("the mapping was never released after the exported blob was dropped — " +
			"the zero above proves nothing, because the cleanup does not fire in this environment")
	}
}
