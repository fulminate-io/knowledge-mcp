package searchengine

import (
	"os"
	"testing"
)

// TestMergeLeavesNoScratchFileOnSuccessOrError pins the half of the merge
// contract that moved into this package: the engine creates the merge file, and
// the engine disposes of it — on the path where the merge succeeds AND on the
// path where it fails after the file already exists.
//
// IT IS THE RE-POINTED SUCCESSOR of the format-level test that asserted bm25
// unlinked its own temp file. Ownership moved, so the assertion moved with it.
// The other half of that old test — that the merged payload was HEAP-backed —
// did not move: it INVERTS, and lands in segmentdist as
// TestMergedPayloadIsMappingBackedNotHeapBacked, where the real mapping hook is
// wired.
//
// IT DELIBERATELY DOES NOT ASSERT THE PAYLOAD IS HEAP-BACKED. Under this
// package's default MapBlob it will be, but asserting that here would pin the
// DEFAULT rather than the property, and would go red against correct production
// wiring the moment a real mapping hook is supplied.
//
// THE SCRATCH DIRECTORY IS THE TEST'S OWN, not TMPDIR. Counting files in a shared
// location would let a concurrent test's scratch file make this count lie in
// either direction.
func TestMergeLeavesNoScratchFileOnSuccessOrError(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		scratch := t.TempDir()
		e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs:     1 << 20,
			DeletesPctAllowed:  MergeDisabledDeadRatio,
			SegmentCountTarget: MergeDisabledCountTarget,
			ScratchDir:         scratch,
		}))
		defer e.Close()

		ids := groupIDsFor(t, 0, 8)
		constituent := sealSegment(t, e, ids, "alpha")
		requireScratchEmpty(t, scratch, "sealing a segment must not create a merge scratch file")

		published, err := e.ReplaceBucket(0, 1, []SegmentID{constituent}, nil, nil)
		if err != nil {
			t.Fatalf("ReplaceBucket: %v", err)
		}
		if published == "" {
			t.Fatal("the consolidation published nothing, so no merge ran and the count below proves nothing")
		}
		requireScratchEmpty(t, scratch, "the merge left its scratch file behind on the SUCCESS path")
	})

	t.Run("error", func(t *testing.T) {
		scratch := t.TempDir()
		// failAt 1 fails the FIRST MergeTo, which is after the engine has created
		// the destination — the only window in which a leak is possible.
		fmtGate := &gateFormat{failAt: 1}
		e := closeOnCleanup(t, New[mockQuery, mockStats](fmtGate, Options{
			MinSegmentDocs:     1 << 20,
			DeletesPctAllowed:  MergeDisabledDeadRatio,
			SegmentCountTarget: MergeDisabledCountTarget,
			ScratchDir:         scratch,
		}))
		defer e.Close()

		ids := groupIDsFor(t, 0, 8)
		constituent := sealSegment(t, e, ids, "alpha")
		before := residentSnapshot(e)
		requireScratchEmpty(t, scratch, "sealing a segment must not create a merge scratch file")

		published, err := e.ReplaceBucket(0, 1, []SegmentID{constituent}, nil, nil)
		if err == nil {
			t.Fatal("the injected merge failure must surface as an error")
		}
		if published != "" {
			t.Fatalf("a failed merge published %s", published)
		}
		if after := residentSnapshot(e); len(after) != len(before) {
			t.Fatalf("resident set changed after a failed merge: %d segments, want %d", len(after), len(before))
		}
		requireScratchEmpty(t, scratch, "the merge left its scratch file behind on the ERROR path")
	})
}

// requireScratchEmpty fails unless dir holds no entries.
func requireScratchEmpty(t *testing.T, dir, why string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the scratch directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s: scratch directory holds %v", why, names)
	}
}
