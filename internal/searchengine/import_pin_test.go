package searchengine

import (
	"encoding/json"
	"testing"
)

// TestImportedEntryPinsTheMemoryItBorrows pins the RULE, not the reproduction: an
// entry whose payload reads memory ANOTHER entry owns must keep that owner
// reachable.
//
// THE RULE MATTERS BECAUSE Export HANDS OUT BORROWED MEMORY. On a mapping-backed
// segment Encode returns the mapping itself rather than a copy, so an exported
// blob's Bytes are the exporting entry's mapping, and the blob carries keepAlive
// to say so. An Import of that blob decodes a NEW payload over those same bytes.
// The unmap is keyed on the EXPORTER's reachability, and holding the bytes does
// not make the exporter reachable — so an import that dropped the pin would read
// unmapped memory the moment the exporter was collected.
//
// IT ASSERTS THE PIN RATHER THAN RACING THE COLLECTOR. The observable failure is
// a segmentation fault that depends on a cleanup running at the wrong moment, and
// a test that tries to provoke that is testing the collector's schedule. The
// reference's presence IS the property, and it is checkable exactly.
//
// The live consequence is already covered elsewhere and is what found this: with
// the pin dropped, the segmentdist suite faults deterministically when a reset
// swap replaces a whole segment set while a merge reads segments imported from it.
func TestImportedEntryPinsTheMemoryItBorrows(t *testing.T) {
	t.Parallel()

	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1}))
	defer e.Close()

	bytes, err := json.Marshal([]mockRow{{ID: "a", Content: "alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	seg, err := mockFormat{}.Decode(bytes)
	if err != nil {
		t.Fatal(err)
	}

	// owner stands for the entry that actually owns the memory the bytes live in.
	// Its identity is all that matters: the import must hold a reference to it.
	owner := new(int)

	borrowed := e.entryFromDecoded(seg, SegmentBlob{ID: "borrowed", Bytes: bytes, keepAlive: owner}, nil)
	if borrowed.pin == nil {
		t.Fatal("an imported entry dropped the pin on the memory its payload borrows — " +
			"the owner can be collected and unmapped while this entry is still reading it")
	}
	if got, ok := borrowed.pin.(*int); !ok || got != owner {
		t.Fatalf("the imported entry pinned %v, want the blob's own owner", borrowed.pin)
	}

	// CONTROL, same run: a blob that owns its bytes outright pins nothing, so the
	// assertion above is about carrying a REAL reference rather than about setting
	// the field to something non-nil.
	owned := e.entryFromDecoded(seg, SegmentBlob{ID: "owned", Bytes: bytes}, nil)
	if owned.pin != nil {
		t.Fatalf("a heap-owned blob's entry pinned %v, want nothing to pin", owned.pin)
	}
}
