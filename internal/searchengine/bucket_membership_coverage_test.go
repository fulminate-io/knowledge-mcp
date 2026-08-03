package searchengine

import "testing"

// bucket_membership_coverage_test.go covers LiveMembersOutside, the containment
// probe a caller consults before dropping a segment.
//
// The four legs are not variations on one another. The known-positive leg is what
// makes the two zero legs meaningful — without it, a probe that never resolved
// anything at all would report "no cell" everywhere and look identical to a
// converged corpus. The dead-member leg is the one that separates LIVENESS from
// bare membership: an implementation that counted route presence alone passes every
// other leg and still holds a segment resident for documents no reader can reach.

// TestLiveMembersOutsideCountsUncoveredOnly pins what the probe counts and what it
// deliberately omits.
func TestLiveMembersOutsideCountsUncoveredOnly(t *testing.T) {
	t.Run("known-positive: an uncovered live member is reported", func(t *testing.T) {
		e := bucketTestEngine(nil)
		defer e.Close()

		taken := map[string]bool{}
		a := doc(idForBucket(t, 0, taken), "alpha")
		b := doc(idForBucket(t, 0, taken), "beta")
		if err := e.Add([]Document{a, b}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		segs := e.Export()
		if len(segs) != 1 {
			t.Fatalf("Export = %d segments, want 1 holding both documents", len(segs))
		}

		got := e.LiveMembersOutside([]SegmentID{segs[0].ID}, map[ExternalID]bool{a.ID: true})
		if got[segs[0].ID] != 1 {
			t.Fatalf("LiveMembersOutside = %v, want a cell of 1 for the uncovered member %q", got, b.ID)
		}
	})

	t.Run("fully covered segments are omitted", func(t *testing.T) {
		e := bucketTestEngine(nil)
		defer e.Close()

		taken := map[string]bool{}
		a := doc(idForBucket(t, 0, taken), "alpha")
		b := doc(idForBucket(t, 0, taken), "beta")
		if err := e.Add([]Document{a, b}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		segs := e.Export()
		if len(segs) != 1 {
			t.Fatalf("Export = %d segments, want 1", len(segs))
		}

		got := e.LiveMembersOutside([]SegmentID{segs[0].ID}, map[ExternalID]bool{a.ID: true, b.ID: true})
		if len(got) != 0 {
			t.Fatalf("LiveMembersOutside = %v, want no cell — every member is covered", got)
		}
	})

	t.Run("dead members do not count", func(t *testing.T) {
		e := bucketTestEngine(nil)
		defer e.Close()

		taken := map[string]bool{}
		a := doc(idForBucket(t, 0, taken), "alpha")
		b := doc(idForBucket(t, 0, taken), "beta")
		if err := e.Add([]Document{a, b}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		segs := e.Export()
		if len(segs) != 1 {
			t.Fatalf("Export = %d segments, want 1", len(segs))
		}
		// b keeps its route and members entries and loses only its live bit, so a
		// probe reading membership alone still reports a cell here.
		e.Delete(b.ID)

		got := e.LiveMembersOutside([]SegmentID{segs[0].ID}, map[ExternalID]bool{a.ID: true})
		if len(got) != 0 {
			t.Fatalf("LiveMembersOutside = %v, want no cell — the only uncovered member is deleted", got)
		}
	})

	t.Run("an unresolvable id contributes no cell", func(t *testing.T) {
		e := bucketTestEngine(nil)
		defer e.Close()

		taken := map[string]bool{}
		a := doc(idForBucket(t, 0, taken), "alpha")
		if err := e.Add([]Document{a}); err != nil {
			t.Fatalf("Add: %v", err)
		}

		got := e.LiveMembersOutside([]SegmentID{"not-a-resident-segment"}, nil)
		if len(got) != 0 {
			t.Fatalf("LiveMembersOutside = %v, want no cell for an id nothing resolves", got)
		}
	})
}
