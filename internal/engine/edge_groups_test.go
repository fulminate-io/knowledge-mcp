// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// expectGroup is the full expectation for one reconstructed group. EVERY field is
// asserted in every subtest that expects a group: a subtest asserting only the
// group COUNT cannot tell a correct partition from one that bucketed every edge
// together.
type expectGroup struct {
	fromID   string
	key      string
	declared int
	members  []string // ToIds, in the order the group must present them
	closed   bool
	complete bool
}

// caller is the single fixture source every reconstruction case groups from.
const caller = "a/x.go:Caller"

// groupEdge builds one fixture edge. Keys are written in the four-component form
// the plan's vocabulary block fixes: file:refByte:edgeType:target.
func groupEdge(to, method, evidence string, confidence float64) knowledgev1.Edge {
	return knowledgev1.Edge{
		FromId:     caller,
		ToId:       to,
		Type:       string(kgtypes.EdgeCalls),
		Method:     method,
		Evidence:   evidence,
		Confidence: confidence,
	}
}

func TestEdgeGroups_Reconstruct(t *testing.T) {
	cases := []struct {
		name string
		// edges is the flat slice as it arrives from a traversal response.
		edges []knowledgev1.Edge
		// wantGroups is the exact expected group list, in the exact expected order.
		wantGroups []expectGroup
		// wantPassthrough is the exact expected ungrouped remainder, by ToId, in
		// the exact expected order.
		wantPassthrough []string
	}{
		{
			// The shape EVERY edge in EVERY graph has today: no Method, no
			// Evidence. Nothing groups, and the remainder is the input verbatim,
			// in input order.
			name: "bound_edges_are_not_grouped",
			edges: []knowledgev1.Edge{
				groupEdge("p/z.go:Run", "", "", 0),
				groupEdge("p/a.go:Run", "", "", 0),
				groupEdge("p/m.go:Run", "", "", 0),
			},
			wantGroups:      nil,
			wantPassthrough: []string{"p/z.go:Run", "p/a.go:Run", "p/m.go:Run"},
		},
		{
			// THE KNOWN-POSITIVE CONTROL for every zero above and below: a
			// GroupCandidateEdges that returned nil unconditionally would pass
			// every absence subtest in this table but fail this one.
			name: "ambiguous_group_collapses",
			edges: []knowledgev1.Edge{
				groupEdge("p/z.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:1042:CALLS:Run", 1.0/3.0),
				groupEdge("p/a.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:1042:CALLS:Run", 1.0/3.0),
				groupEdge("p/m.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:1042:CALLS:Run", 1.0/3.0),
			},
			wantGroups: []expectGroup{{
				fromID:   caller,
				key:      "a/x.go:1042:CALLS:Run",
				declared: 3,
				members:  []string{"p/a.go:Run", "p/m.go:Run", "p/z.go:Run"}, // sorted by ToId
				closed:   true,
				complete: true,
			}},
			wantPassthrough: nil,
		},
		{
			name: "dynamic_group_is_open",
			edges: []knowledgev1.Edge{
				groupEdge("p/b.go:Send", kgtypes.EdgeMethodDynamic, "a/x.go:77:CALLS:Send", 0.5),
				groupEdge("p/a.go:Send", kgtypes.EdgeMethodDynamic, "a/x.go:77:CALLS:Send", 0.5),
			},
			wantGroups: []expectGroup{{
				fromID:   caller,
				key:      "a/x.go:77:CALLS:Send",
				declared: 2,
				members:  []string{"p/a.go:Send", "p/b.go:Send"},
				closed:   false, // dynamic is OPEN: one of these, or beyond
				complete: true,
			}},
			wantPassthrough: nil,
		},
		{
			// THE CATCHER for an implementation that treats CARDINALITY as the
			// discriminant. A one-candidate dynamic dispatch is still open-set;
			// rendering it as a bound call would assert a certainty the collector
			// never claimed.
			name: "dynamic_group_of_one_is_still_a_group",
			edges: []knowledgev1.Edge{
				groupEdge("p/only.go:Send", kgtypes.EdgeMethodDynamic, "a/x.go:88:CALLS:Send", 1.0),
			},
			wantGroups: []expectGroup{{
				fromID:   caller,
				key:      "a/x.go:88:CALLS:Send",
				declared: 1,
				members:  []string{"p/only.go:Send"},
				closed:   false,
				complete: true,
			}},
			wantPassthrough: nil, // and NOT in the passthrough slice
		},
		{
			// THE TICKET'S NAMED NEGATIVE CONTROL, verbatim: "two DIFFERENT
			// references to overlapping candidate sets stay distinct groups".
			// The two keys differ ONLY in the refByte component. p/b.go:Run is in
			// BOTH candidate sets and must appear once in each group, never twice
			// in one. The overall member sets differ so that a collapse-into-one
			// bug is distinguishable from correct behavior.
			name: "distinct_references_stay_distinct",
			edges: []knowledgev1.Edge{
				groupEdge("p/b.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:100:CALLS:Run", 0.5),
				groupEdge("p/a.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:100:CALLS:Run", 0.5),
				groupEdge("p/d.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:220:CALLS:Run", 1.0/3.0),
				groupEdge("p/b.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:220:CALLS:Run", 1.0/3.0),
				groupEdge("p/c.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:220:CALLS:Run", 1.0/3.0),
			},
			wantGroups: []expectGroup{
				{
					fromID:   caller,
					key:      "a/x.go:100:CALLS:Run",
					declared: 2,
					members:  []string{"p/a.go:Run", "p/b.go:Run"},
					closed:   true,
					complete: true,
				},
				{
					fromID:   caller,
					key:      "a/x.go:220:CALLS:Run",
					declared: 3,
					members:  []string{"p/b.go:Run", "p/c.go:Run", "p/d.go:Run"},
					closed:   true,
					complete: true,
				},
			},
			wantPassthrough: nil,
		},
		{
			// THE CATCHER for a detector keying on "Evidence is non-empty" rather
			// than on Method. This is the real shape a cloud/linkage edge has —
			// the exact strings are seeded by
			// cmd/knowledge-server/internal/store/graph_serializer_test.go.
			name: "evidence_without_group_method_passes_through",
			edges: []knowledgev1.Edge{
				groupEdge("img/base:latest", "tier1-dockerfile", "Dockerfile:14 COPY src", 0.9),
			},
			wantGroups:      nil,
			wantPassthrough: []string{"img/base:latest"},
		},
		{
			// A candidate Method with no group key is a producer contract
			// violation. It must SURFACE as an ordinary edge, never be dropped —
			// dropping it would hide an emitter bug behind a render that looks
			// correct.
			name: "group_method_without_evidence_passes_through_and_warns",
			edges: []knowledgev1.Edge{
				groupEdge("p/orphan.go:Run", kgtypes.EdgeMethodAmbiguousName, "", 0.5),
			},
			wantGroups:      nil,
			wantPassthrough: []string{"p/orphan.go:Run"},
		},
		{
			// The exact signal Phase 3's enrichment triggers on: five candidates
			// were declared, only two were observed. Pinned here rather than only
			// where it is consumed.
			name: "n_recovered_from_confidence",
			edges: []knowledgev1.Edge{
				groupEdge("p/b.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:9:CALLS:Run", 1.0/5.0),
				groupEdge("p/a.go:Run", kgtypes.EdgeMethodAmbiguousName, "a/x.go:9:CALLS:Run", 1.0/5.0),
			},
			wantGroups: []expectGroup{{
				fromID:   caller,
				key:      "a/x.go:9:CALLS:Run",
				declared: 5,
				members:  []string{"p/a.go:Run", "p/b.go:Run"},
				closed:   true,
				complete: false, // 2 observed of 5 declared
			}},
			wantPassthrough: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups, passthrough := GroupCandidateEdges(tc.edges)

			if len(groups) != len(tc.wantGroups) {
				t.Fatalf("group count = %d, want %d (groups: %+v)", len(groups), len(tc.wantGroups), groups)
			}
			for i, want := range tc.wantGroups {
				got := groups[i]
				if got.FromID != want.fromID {
					t.Errorf("group[%d].FromID = %q, want %q", i, got.FromID, want.fromID)
				}
				if got.Key != want.key {
					t.Errorf("group[%d].Key = %q, want %q", i, got.Key, want.key)
				}
				if got.Declared != want.declared {
					t.Errorf("group[%d].Declared = %d, want %d", i, got.Declared, want.declared)
				}
				if got.Closed() != want.closed {
					t.Errorf("group[%d].Closed() = %v, want %v", i, got.Closed(), want.closed)
				}
				if got.Complete() != want.complete {
					t.Errorf("group[%d].Complete() = %v, want %v", i, got.Complete(), want.complete)
				}
				if len(got.Members) != len(want.members) {
					t.Fatalf("group[%d] member count = %d, want %d", i, len(got.Members), len(want.members))
				}
				for j, wantTo := range want.members {
					if got.Members[j].ToId != wantTo {
						t.Errorf("group[%d].Members[%d].ToId = %q, want %q", i, j, got.Members[j].ToId, wantTo)
					}
				}
			}

			if len(passthrough) != len(tc.wantPassthrough) {
				t.Fatalf("passthrough count = %d, want %d (%+v)", len(passthrough), len(tc.wantPassthrough), passthrough)
			}
			for i, wantTo := range tc.wantPassthrough {
				if passthrough[i].ToId != wantTo {
					t.Errorf("passthrough[%d].ToId = %q, want %q", i, passthrough[i].ToId, wantTo)
				}
			}
		})
	}
}
