// SPDX-License-Identifier: Apache-2.0

package workingset

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestHasIsDestinationInsensitive pins what Set.Has means: "does this process
// maintain (gt, name) AT ALL", across every storage copy. HasRef is the
// exact-copy predicate and stays one.
//
// THE DISTINCTION IS LOAD-BEARING RATHER THAN PEDANTIC, and it was a latent
// defect before it was a visible one. Has is the predicate the background LOOPS
// are gated on — the thought-propagation loop's working-set gate and the
// manage(status) coverage table's membership cell are its two production
// callers — and a loop gate asks a graph-level question: is this a graph we
// work on. It answered by looking up a Ref with an EMPTY storage field, so it
// reported false for a member admitted by any destination-aware route. Every
// admission route except the segment search has recorded a destination-bearing
// Ref since the per-destination pools landed, so the propagation loop was
// already gated shut for a graph admitted by a routed call or a collect; making
// the search route destination-aware too would have closed the last route that
// happened to answer true, turning a latent defect into a live one.
//
// THE FIX IS HERE AND NOT AT THE CALL SITES, because both callers want the same
// graph-level answer and neither has a destination in hand to offer.
func TestHasIsDestinationInsensitive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		admit   Ref
		wantRef Ref
	}{
		{
			name:    "local destination copy",
			admit:   Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"},
			wantRef: Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"},
		},
		{
			name:    "signed in account copy",
			admit:   Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "cloud", Account: "acct-1"},
			wantRef: Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "cloud", Account: "acct-1"},
		},
		{
			name:    "destination-less copy",
			admit:   Ref{GraphType: kgtypes.GraphKnowledge, Name: "default"},
			wantRef: Ref{GraphType: kgtypes.GraphKnowledge, Name: "default"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			if !s.AdmitRef(tc.admit, "test") {
				t.Fatalf("fixture check: AdmitRef(%+v) reported no new member", tc.admit)
			}
			if !s.Has(kgtypes.GraphKnowledge, "default") {
				t.Errorf("Has(knowledge, default) = false after admitting %+v — Has is the graph-level "+
					"predicate the background loops are gated on, so a member under ANY destination "+
					"must satisfy it; members=%+v", tc.admit, s.Members())
			}
			// HasRef STAYS EXACT, and that is the half a destination-insensitive Has
			// must not swallow: the per-destination pools exist so two copies of one
			// graph can be maintained independently.
			if !s.HasRef(tc.wantRef) {
				t.Errorf("HasRef(%+v) = false for the copy just admitted", tc.wantRef)
			}
			other := Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "somewhere-else"}
			if s.HasRef(other) {
				t.Errorf("HasRef(%+v) = true, but only %+v was admitted — HasRef must stay the "+
					"EXACT-COPY predicate", other, tc.admit)
			}
		})
	}

	// AND THE NEGATIVE, so the assertions above are not satisfied by a Has that
	// answers true for everything.
	t.Run("a_graph_nobody_admitted_is_not_a_member", func(t *testing.T) {
		s := New()
		s.AdmitRef(Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"}, "test")
		if s.Has(kgtypes.GraphCode, "never-admitted") {
			t.Error("Has(code, never-admitted) = true on a set holding only knowledge/default — " +
				"a destination-insensitive Has must still be a membership test")
		}
		if s.Has(kgtypes.GraphKnowledge, "other-name") {
			t.Error("Has(knowledge, other-name) = true — the NAME must still discriminate")
		}
	})
}
