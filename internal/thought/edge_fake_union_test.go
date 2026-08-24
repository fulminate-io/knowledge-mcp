// SPDX-License-Identifier: Apache-2.0

package thought

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// unionEdgesForRequest is the ONE rule the under-serving edge fakes in this package
// answers a RETURN_MODE_EDGES request with: return every seeded edge whose type is in
// the requested set, with an EMPTY requested set meaning every type, narrowed by
// node-set incidence when the plan carries ids, and narrowed by the half-open from_id
// band when the plan carries one.
//
// WHY IT EXISTS. Several fakes historically picked a SINGLE bucket by first match —
// "if the request mentions charged-by, return the charge edges" — which is adequate
// only while every caller asks for one type at a time. The unified pivot read asks for
// seven at once, and under a first-match arm such a fake returns a strict subset and
// silently starves its consumer. That starvation is invisible when the affected test's
// assertions never reach the dropped content, which is exactly how it survived twice
// in this ticket.
//
// THE SEMANTICS ARE LIFTED from reflectEquivFake.edgesFor (corpus_equivalence_test.go),
// which already models the wire correctly: requested-type filter with empty-means-all,
// plus node-SET incidence that keeps an edge touching ANY requested id.
//
// NO STARVATION PANIC. The plan prescribed one — panic when a multi-type request
// against a non-empty seed yields nothing — and it was built, executed, and REMOVED
// because it is unsound in both directions. It false-fires on a legitimate empty
// intersection: TestContextPack_ChargeStateAttached seeds only a charged-by edge and
// the composer's expand read asks for five OTHER types, so zero matches is the correct
// answer and the panic aborted a passing test. And it cannot catch what it was for —
// a fake that does not route through this helper never calls it, so no panic here can
// observe that fake's starvation. The PROPERTY TEST is the guard that actually works,
// because it drives each fake explicitly and asserts on what comes back.
//
// This file is DUPLICATED verbatim in cmd/knowledge/internal/tools. Go test symbols
// are not importable across packages, and the architecture invariant forbids a new
// hand-written shared package — the only sanctioned cross-module contract is generated
// protobuf, which this is not. The same reasoning is already recorded in-tree at
// thought_ondemand_corpus_test.go: the shape is the reuse, not the code.
func unionEdgesForRequest(seed []*knowledgev1.Edge, q *knowledgev1.QueryPlan) []*knowledgev1.Edge {
	types := q.GetSelection().GetEdgeTypes()
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}

	inScope := map[string]bool{}
	for _, id := range q.GetIds() {
		inScope[id] = true
	}

	var out []*knowledgev1.Edge
	for _, e := range seed {
		if len(want) > 0 && !want[e.GetType()] {
			continue
		}
		// Node-SET carrier semantics: keep an edge incident to ANY requested id.
		if len(inScope) > 0 && !inScope[e.GetFromId()] && !inScope[e.GetToId()] {
			continue
		}
		out = append(out, e)
	}
	// The band is applied through the SAME helper every other fake in this package
	// wraps its answer with, so there is exactly ONE band predicate here.
	return bandNarrow(out, q)
}

// bandNarrow drops any edge whose from_id falls outside the plan's half-open
// [from_id_gte, from_id_lt) band, and returns edges UNCHANGED when the plan carries
// no band at all. It is the band half of the fake-server answer rule, split out so
// every fake in the package can wrap its own response with one call:
//
//	return &knowledgev1.ExecuteResponse{Edges: bandNarrow(<whatever it built>, q)}, nil
//
// WHY EVERY FAKE NEEDS IT. A fake that ignores the band is simulating a server built
// before the field existed — one that answers every band with the whole graph. That
// is not a harmless approximation: paging.DrainBandedEdges' out-of-band guard rejects
// such an answer outright and fails the read, exactly as it would against a real
// version-skewed server. Honoring the band here is what keeps these fakes faithful.
//
// RETURNING THE INPUT UNCHANGED FOR AN UNBANDED PLAN is what makes the wrap safe to
// apply at a site whose reader is not banded, so a fake need not know which of its
// callers band and which do not.
//
// The half-open rule is deliberately IDENTICAL to paging's own inBand — inclusive
// lower, exclusive upper, empty meaning unbounded. Fifteen inline copies of that
// comparison is precisely how the two arms drift apart.
func bandNarrow(edges []*knowledgev1.Edge, q *knowledgev1.QueryPlan) []*knowledgev1.Edge {
	band := q.GetEdgeFromBand()
	if band == nil {
		return edges
	}
	lo, hi := band.GetFromIdGte(), band.GetFromIdLt()
	out := make([]*knowledgev1.Edge, 0, len(edges))
	for _, e := range edges {
		if lo != "" && e.GetFromId() < lo {
			continue
		}
		if hi != "" && e.GetFromId() >= hi {
			continue
		}
		out = append(out, e)
	}
	return out
}
