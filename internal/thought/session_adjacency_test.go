// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sessionAdjacencyFake serves the EdgeKGContains session→member topology for the
// node-SET RETURN_MODE_EDGES query deriveSessionSiblings issues (Ids set, no
// FromId, filtered to EdgeKGContains) — answered with every kg_contains edge
// incident to a requested id (both directions, mirroring Forward=nil). The
// members/sessionsOf maps also feed the test's INLINE legacy-semantics reference
// (legacySessionSiblings) so the bulk derivation is compared against the retired
// per-thought SiblingExpander contract.
//
// bulkEdgeCalls counts the node-SET edges query so the test can assert the
// derivation issues exactly one regardless of thought count.
type sessionAdjacencyFake struct {
	// session → ordered member node IDs (members may be thoughts or, to exercise
	// the pollution guard, non-thought nodes).
	members map[string][]string
	// thought → ordered enclosing sessions (the inverse, for the legacy reference).
	sessionsOf map[string][]string

	bulkEdgeCalls int
}

func (f *sessionAdjacencyFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// (1) node-SET edges query (deriveSessionSiblings): Ids set, EDGES mode.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES && len(q.GetIds()) > 0 {
		f.bulkEdgeCalls++
		want := make(map[string]bool, len(q.GetIds()))
		for _, id := range q.GetIds() {
			want[id] = true
		}
		var edges []*knowledgev1.Edge
		for session, members := range f.members {
			for _, m := range members {
				// session → member kg_contains edge; surface it if EITHER endpoint
				// is in the requested set (both-direction union).
				if want[session] || want[m] {
					edges = append(edges, &knowledgev1.Edge{
						Type:   string(kgtypes.EdgeKGContains),
						FromId: session,
						ToId:   m,
					})
				}
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: edges}, nil
	}

	return &knowledgev1.ExecuteResponse{}, nil
}

// legacySessionSiblings replays the RETIRED per-thought SiblingExpander contract
// directly over the fixture topology: for each session a thought belongs to, every
// OTHER member m (m != nodeID && idSet[m]) is a sibling. Duplicates across sessions
// are preserved (the legacy walk appended per session) so the comparison is a
// MULTISET, matching the bulk derivation's multiplicity.
func legacySessionSiblings(f *sessionAdjacencyFake, nodeID string, idSet map[string]bool) []string {
	var sibs []string
	for _, sid := range f.sessionsOf[nodeID] {
		for _, m := range f.members[sid] {
			if m != nodeID && idSet[m] {
				sibs = append(sibs, m)
			}
		}
	}
	return sibs
}

// sortedSiblingMap returns a copy of sibAdj with each value slice sorted (with
// duplicates preserved — siblings are a MULTISET: a thought sharing two sessions
// with the same sibling produces that sibling twice, and Leiden CPM counts edges,
// so multiplicity is value-affecting and must match between the two derivations).
func sortedSiblingMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		cp := append([]string(nil), v...)
		sort.Strings(cp)
		out[k] = cp
	}
	return out
}

// TestDeriveSessionSiblings_EqualsTraversal (FAILS-WHEN-ABSENT) proves the
// bulk-derived sibling adjacency equals the legacy per-thought traversal sibling
// sets for every thought, excludes a non-thought session member (pollution
// guard), and issues exactly ONE bulk edges Execute for the whole derivation.
func TestDeriveSessionSiblings_EqualsTraversal(t *testing.T) {
	ctx := context.Background()

	// Fixture:
	//   sA = {t1, t2, t3}            — ordinary multi-member session.
	//   sB = {t3, t4}               — shares t3 with sA (two-session member).
	//   sC = {t5}                   — single member: no siblings.
	//   sD = {t6, plan1}            — plan1 is a NON-thought member (pollution).
	fake := &sessionAdjacencyFake{
		members: map[string][]string{
			"sA": {"t1", "t2", "t3"},
			"sB": {"t3", "t4"},
			"sC": {"t5"},
			"sD": {"t6", "plan1"},
		},
		sessionsOf: map[string][]string{
			"t1": {"sA"},
			"t2": {"sA"},
			"t3": {"sA", "sB"},
			"t4": {"sB"},
			"t5": {"sC"},
			"t6": {"sD"},
		},
	}

	// The in-scope thought set EXCLUDES plan1 — the pollution guard must drop it.
	nodeIDs := []string{"t1", "t2", "t3", "t4", "t5", "t6"}
	idSet := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		idSet[id] = true
	}

	// BULK derivation: one edge read + pure group-by.
	got := deriveSessionSiblings(ctx, fake, nodeIDs, idSet)

	// RPC SHAPE: exactly one bulk edges Execute regardless of thought count.
	assert.Equal(t, 1, fake.bulkEdgeCalls,
		"deriveSessionSiblings must issue exactly ONE bulk edges read for the whole derivation")

	// REFERENCE: replay the OLD per-thought session-sibling semantics INLINE over
	// the same fixture topology — walk each thought's sessions, then each session's
	// members, self-excluded and idSet-filtered (exactly the retired SiblingExpander
	// contract). This is the equivalence baseline the bulk derivation must match.
	want := make(map[string][]string)
	for _, id := range nodeIDs {
		for _, sid := range legacySessionSiblings(fake, id, idSet) {
			want[id] = append(want[id], sid)
		}
	}

	// EQUIVALENCE: per-thought sibling MULTISET equality (sorted-with-duplicates).
	assert.Equal(t, sortedSiblingMap(want), sortedSiblingMap(got),
		"bulk-derived siblings must equal the legacy traversal sibling multiset for every thought")

	// Concrete spot-checks.
	assert.ElementsMatch(t, []string{"t2", "t3"}, got["t1"], "t1 siblings via sA")
	// t3 is in sA{t1,t2,t3} and sB{t3,t4} → siblings {t1,t2} from sA + {t4} from sB.
	assert.ElementsMatch(t, []string{"t1", "t2", "t4"}, got["t3"], "t3 siblings span both sessions")
	assert.Empty(t, got["t5"], "single-member session yields no siblings")

	// POLLUTION GUARD: the non-thought member plan1 never appears as a sibling, and
	// t6 (its only co-member) has no siblings once plan1 is filtered out.
	assert.Empty(t, got["t6"], "t6's only co-member is the filtered non-thought plan1")
	for tid, sibs := range got {
		assert.NotContains(t, sibs, "plan1", "the non-thought member must never be a sibling (thought %s)", tid)
	}
}
