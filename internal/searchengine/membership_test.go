// SPDX-License-Identifier: Apache-2.0

package searchengine

import "testing"

// TestResidentMembershipMatchesSearchability pins that membership means
// SEARCHABILITY, not route presence.
//
// The distinction is the whole point: Delete clears an id's live bit but leaves its
// route and members entries in place, so a route-only probe reports a silently
// unsearchable document as covered — the exact state a repair pass exists to find,
// laundered into a green reading.
func TestResidentMembershipMatchesSearchability(t *testing.T) {
	newSealed := func(t *testing.T, ids ...string) *SegmentedIndex[mockQuery, mockStats] {
		t.Helper()
		e := newTestEngine(1)
		t.Cleanup(func() { e.Close() })
		docs := make([]Document, 0, len(ids))
		for _, id := range ids {
			docs = append(docs, doc(id, "content "+id))
		}
		if _, err := e.AddSealAndSupersede(docs); err != nil {
			t.Fatalf("AddSealAndSupersede: %v", err)
		}
		return e
	}

	t.Run("resident_id_true", func(t *testing.T) {
		e := newSealed(t, "a", "b")
		if got := e.UncoveredFrom([]ExternalID{"a"}); len(got) != 0 {
			t.Fatalf("UncoveredFrom(resident) = %v, want empty", got)
		}
	})

	t.Run("unknown_id_false", func(t *testing.T) {
		e := newSealed(t, "a")
		got := e.UncoveredFrom([]ExternalID{"never-added"})
		if len(got) != 1 || got[0] != "never-added" {
			t.Fatalf("UncoveredFrom(unknown) = %v, want [never-added]", got)
		}
	})

	t.Run("deleted_id_false", func(t *testing.T) {
		// The catcher for a route-only predicate: after Delete the id KEEPS its
		// route and members entries and only loses the live bit, so a route probe
		// still reports it present.
		e := newSealed(t, "a", "b")
		e.Delete("a")
		got := e.UncoveredFrom([]ExternalID{"a"})
		if len(got) != 1 || got[0] != "a" {
			t.Fatalf("UncoveredFrom(deleted) = %v, want [a] — a deleted id is NOT searchable", got)
		}
	})

	t.Run("count_agrees_with_uncovered", func(t *testing.T) {
		// The anti-divergence catcher: both answers must derive from the one
		// predicate, so over any fixture the identity below holds. It fails the
		// moment the count and the diff stop sharing residentMemberIn.
		e := newSealed(t, "a", "b", "c", "d")
		e.Delete("b")
		ids := []ExternalID{"a", "b", "c", "d"}
		live := e.LiveResidentCount()
		uncovered := len(e.UncoveredFrom(ids))
		if live != len(ids)-uncovered {
			t.Fatalf("LiveResidentCount()=%d, len(ids)-uncovered=%d — the count and the diff disagree",
				live, len(ids)-uncovered)
		}
		if live != 3 {
			t.Fatalf("LiveResidentCount() = %d, want 3 (four added, one deleted)", live)
		}
	})
}
