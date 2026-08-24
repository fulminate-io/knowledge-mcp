// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// emitImplementsEdges projects a derivation into IMPLEMENTS edges at BOTH
// levels, and logs what the derivation could not decide.
//
// BOTH LEVELS, because the two answer different questions and only together are
// they complete:
//
//	TYPE LEVEL   <interface type declaration>  --IMPLEMENTS-->  <concrete type declaration>
//	METHOD LEVEL <interface method spec>       --IMPLEMENTS-->  <the method satisfying it>
//
// DIRECTION IS FROM THE INTERFACE OUTWARD. A consumer standing on a call's
// target — the interface method, which is where the call graph now points — walks
// out over IMPLEMENTS to reach the implementers in one hop.
//
// A PROMOTED METHOD'S EDGE TARGETS THE DECLARING TYPE'S METHOD NODE, because no
// node exists for the promoting type's copy of it. A type that gains Close()
// through an embedded interface has no Close() declaration of its own to point
// at, and inventing a synthetic node would put an ID in the graph that no source
// location backs.
//
// THESE EDGES DO NOT ENTER resolveReference, AND MUST NOT. Both endpoints are
// already node IDs — the derivation works on declRec, which carries NodeID — so
// they ride the same append path the language-hub edges use, which is exactly the
// class of edge that names both ends up front and has nothing left to resolve.
//
// A METHOD SPEC DOES NOT IMPLEMENT ITSELF, and the method loop suppresses the
// pairs that would say it does. When a concrete type EMBEDS an interface, its
// promoted method set holds that interface's OWN spec records, so m.spec and
// m.impl are one declaration and the projection would emit
// <spec> --IMPLEMENTS--> <spec>. That is a FALSE fact rather than a repeated
// true one, so it is suppressed here rather than collapsed downstream: merging
// duplicates would leave exactly one self-loop per identity, still stored and
// still meaningless.
//
// NOTHING IS LOST BY THE SUPPRESSION. The TYPE-level edge emitted just above
// still names the embedding type as an implementor of the interface, which is
// the fact the pair actually establishes; only the degenerate method-level
// projection of it goes away.
//
// MEASURED, TREE-DERIVED at commit c8afb0f8 over this repository and not a
// locked literal: 1,115 emitted self-loop rows over 92 identities in 5 files,
// one identity emitted 30 times, all of them from types embedding store.DB.
func emitImplementsEdges(ix *declIndex) []*knowledgev1.Edge {
	pairs, stats := deriveImplements(ix)
	if len(pairs) == 0 {
		implementsLog(stats, 0, 0)
		return nil
	}
	// Pre-size from the pair count plus one method edge per method in each
	// pair, rather than growing from nil across thousands of appends.
	size := len(pairs)
	for _, p := range pairs {
		size += len(p.methods)
	}
	out := make([]*knowledgev1.Edge, 0, size)
	selfSatisfied := 0
	for _, p := range pairs {
		method := kgtypes.EdgeMethodMethodSet + strconv.Itoa(p.methodSetSize)
		out = append(out, implementsEdge(p.iface.NodeID, p.concrete.NodeID, method))
		for _, m := range p.methods {
			if m.spec.NodeID == m.impl.NodeID {
				selfSatisfied++
				continue
			}
			out = append(out, implementsEdge(m.spec.NodeID, m.impl.NodeID, method))
		}
	}
	implementsLog(stats, len(out), selfSatisfied)
	return out
}

// implementsEdge builds one IMPLEMENTS edge.
//
// WEIGHT STAYS 0, matching CONTAINS, IMPORTS, USES_TYPE and EMBEDS — every edge
// type for which aggregation does not apply. The method-set cardinality rides
// Method instead, and the reason is not stylistic: the weighted topology
// analyzers normalize a zero weight to the 1.0 baseline, so a cardinality on
// Weight would give the LOW-information single-method edges exactly an ordinary
// edge's strength while amplifying the large-interface ones — the opposite of
// what the cardinality is published for. kgtypes.EdgeMethodMethodSet documents
// the full reasoning.
func implementsEdge(from, to, method string) *knowledgev1.Edge {
	return &knowledgev1.Edge{
		FromId: from,
		ToId:   to,
		Type:   string(kgtypes.EdgeImplements),
		Method: method,
	}
}

// implementsLog reports the derivation's summary, and above all what it could
// NOT decide.
//
// THE UNDECIDED COUNTS ARE THE POINT. "No implementers" and "could not be
// decided" are different facts about an interface, and a graph shows the same
// thing for both — nothing. Only a counter can tell them apart, so the generic
// skip count and the under-known-method-set pair count are reported beside the
// totals rather than folded into them.
//
// self_satisfied_suppressed IS THAT SAME RULE APPLIED TO THE SUPPRESSION. A
// method-level pair skipped because its spec and its impl are one declaration
// leaves no row behind, and a graph shows the same nothing for "suppressed" as
// for "never derived". Reporting the count is what keeps the suppression
// attributable in a real collect.
func implementsLog(stats implementsStats, edges, selfSatisfied int) {
	slog.Info("collector: interface satisfaction",
		"interfaces_decided", stats.Interfaces,
		"pairs", stats.Pairs,
		"edges", edges,
		"self_satisfied_suppressed", selfSatisfied,
		"generic_undecided", stats.GenericUndecided,
		"extembed_pairs", stats.ExtEmbedPairs)
}
