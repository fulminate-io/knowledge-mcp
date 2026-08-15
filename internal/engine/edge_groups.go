// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"log/slog"
	"math"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// edge_groups.go reconstructs multi-candidate edge groups at RENDER time.
//
// The collector emits one edge per candidate when a reference cannot be bound to
// a single declaration. Every member of one group shares a key in Edge.Evidence
// (the reference site's identity) and carries Edge.Method set to one of the two
// kgtypes group-method constants, with Edge.Confidence = 1/N.
//
// THE GROUP KEY IS OPAQUE HERE. Nothing in this package parses it, splits it, or
// depends on how many components it has; it is only ever compared for equality
// and printed verbatim. The producer is free to change its composition without
// touching a renderer.

// CandidateGroup is the render-time reconstruction of one multi-candidate
// reference: N edges that together mean ONE reference whose target could not be
// bound to a single declaration.
type CandidateGroup struct {
	// FromID is the edge source — the declaration holding the reference.
	FromID string
	// EdgeType is e.Type verbatim: CALLS, USES_TYPE or EMBEDS.
	EdgeType string
	// Method is kgtypes.EdgeMethodAmbiguousName or kgtypes.EdgeMethodDynamic.
	Method string
	// Key is the Evidence group key. Rendered opaquely and NEVER parsed.
	Key string
	// Members are the observed member edges, sorted by ToId. Values, not
	// pointers: see copyGroupEdge.
	Members []knowledgev1.Edge
	// Declared is N as recovered from Confidence; 0 when unrecoverable. A
	// group whose Declared exceeds len(Members) was observed incompletely.
	Declared int
}

// copyGroupEdge returns a fresh knowledgev1.Edge with the same field values as e.
// The proto knowledgev1.Edge carries a noCopy MessageState, so `dst = *e` is a
// copylocks violation; a field-by-field literal is the lock-clean way to
// materialize a copy. This is the same idiom, for the same reason, as copyEdge in
// cmd/knowledge/internal/tools/intercept_query_explain_timeline.go — package
// engine cannot import package tools, so the helper is declared twice rather
// than shared.
func copyGroupEdge(e *knowledgev1.Edge) knowledgev1.Edge {
	return knowledgev1.Edge{
		FromId:        e.FromId,
		ToId:          e.ToId,
		Type:          e.Type,
		Weight:        e.Weight,
		Confidence:    e.Confidence,
		Method:        e.Method,
		Evidence:      e.Evidence,
		LastValidated: e.LastValidated,
	}
}

// IsCandidateEdge reports whether e is a member of a multi-candidate group.
//
// IT KEYS ON Method ALONE — deliberately, on two counts:
//
// It does NOT look at Evidence. Other graphs populate Evidence for unrelated
// purposes (a store test seeds "Dockerfile:14 COPY src" with Method
// "tier1-dockerfile"; the k8s exposure fixture marshals JSON into Evidence), so
// "Evidence is non-empty" would misclassify them as group members.
//
// It does NOT look at e.Type. Keying on Type would need an EMBEDS constant that
// engine deliberately does not have: EdgeEmbeds is declared only in
// cmd/knowledge/internal/collector/treesitter/types.go, and a render package must
// not import the collector. Method-keying makes the detector edge-type agnostic,
// which is what lets CALLS, USES_TYPE and EMBEDS groups all work here.
func IsCandidateEdge(e *knowledgev1.Edge) bool {
	if e == nil {
		return false
	}
	return e.Method == kgtypes.EdgeMethodAmbiguousName || e.Method == kgtypes.EdgeMethodDynamic
}

// GroupCandidateEdges partitions edges into reconstructed candidate groups and
// the ungrouped remainder, in ONE pass.
//
// Group identity is the pair (FromID, Evidence). The edge type is already a
// component of the Evidence key, so adding e.Type to the tuple would be
// redundant.
//
// A candidate edge with an EMPTY Evidence is a producer contract violation this
// renderer cannot group. It is NOT dropped and NO key is invented for it: it
// passes through as an ordinary edge and is warned about once. Dropping it would
// hide an emitter bug behind a render that looks correct.
//
// Returned groups are sorted by (FromID, Key); Members within each group are
// sorted by ToId. Passthrough edges keep input order.
func GroupCandidateEdges(edges []knowledgev1.Edge) ([]CandidateGroup, []knowledgev1.Edge) {
	type groupKey struct {
		from     string
		evidence string
	}

	buckets := make(map[groupKey]*CandidateGroup, len(edges))
	order := make([]groupKey, 0, len(edges))
	passthrough := make([]knowledgev1.Edge, 0, len(edges))

	for i := range edges {
		e := &edges[i]
		if !IsCandidateEdge(e) {
			passthrough = append(passthrough, copyGroupEdge(e))
			continue
		}
		if e.Evidence == "" {
			slog.Warn("candidate edge carries no group key; rendering it as an ordinary edge",
				"from_id", e.FromId, "to_id", e.ToId, "method", e.Method)
			passthrough = append(passthrough, copyGroupEdge(e))
			continue
		}
		k := groupKey{from: e.FromId, evidence: e.Evidence}
		g, ok := buckets[k]
		if !ok {
			g = &CandidateGroup{
				FromID:   e.FromId,
				EdgeType: e.Type,
				Method:   e.Method,
				Key:      e.Evidence,
				Declared: declaredFromConfidence(e.Confidence),
			}
			buckets[k] = g
			order = append(order, k)
		}
		g.Members = append(g.Members, copyGroupEdge(e))
	}

	groups := make([]CandidateGroup, 0, len(order))
	for _, k := range order {
		g := buckets[k]
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].ToId < g.Members[j].ToId })
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].FromID != groups[j].FromID {
			return groups[i].FromID < groups[j].FromID
		}
		return groups[i].Key < groups[j].Key
	})

	return groups, passthrough
}

// declaredFromConfidence recovers N from a per-edge confidence of 1/N. Returns 0
// when confidence is non-positive, which is what makes an unrecoverable count
// distinguishable from a real one rather than silently rendering as a size.
func declaredFromConfidence(confidence float64) int {
	if confidence <= 0 {
		return 0
	}
	return int(math.Round(1 / confidence))
}

// Closed reports whether the group is CLOSED — the reference means exactly one of
// these candidates. Only ambiguous-name groups are closed; a dynamic group is
// open, because dynamic dispatch can reach a target no static enumeration can
// see. METHOD IS THE DISCRIMINANT, CARDINALITY IS NOT: a dynamic group with one
// member is still an open group, never a bound edge.
func (g CandidateGroup) Closed() bool {
	return g.Method == kgtypes.EdgeMethodAmbiguousName
}

// Complete reports whether every declared candidate was observed. A group read
// off a reverse walk can be incomplete: the walk sees only the members pointing
// at the node it started from.
func (g CandidateGroup) Complete() bool {
	return g.Declared > 0 && len(g.Members) == g.Declared
}

// ConfidenceEach returns the per-candidate confidence (1/N), or 0 for an empty
// group.
func (g CandidateGroup) ConfidenceEach() float64 {
	if len(g.Members) == 0 {
		return 0
	}
	return g.Members[0].Confidence
}

// boundAdjacency builds the undirected adjacency over the NON-group edges — group
// edges are the frontier and are never walked through — plus the set of every
// node the edge carrier mentions at all. An edge is followable from whichever
// endpoint the walk arrived at, matching the server's own both-union.
func boundAdjacency(edges []knowledgev1.Edge, groups []CandidateGroup) (map[string][]string, map[string]bool) {
	members := make(map[edgeIdent]bool, len(edges))
	for gi := range groups {
		for mi := range groups[gi].Members {
			members[identOf(&groups[gi].Members[mi])] = true
		}
	}
	adj := make(map[string][]string, len(edges))
	endpoints := make(map[string]bool, len(edges))
	for i := range edges {
		e := &edges[i]
		endpoints[e.FromId] = true
		endpoints[e.ToId] = true
		if members[identOf(e)] {
			continue
		}
		adj[e.FromId] = append(adj[e.FromId], e.ToId)
		adj[e.ToId] = append(adj[e.ToId], e.FromId)
	}
	return adj, endpoints
}

// reachableOver is the BFS from start over the bound-edge adjacency.
func reachableOver(start string, adj map[string][]string) map[string]bool {
	reachable := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if !reachable[nb] {
				reachable[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	return reachable
}

// frontierOf collects the candidates AT the reachable frontier. SYMMETRIC on
// purpose: for a member edge with either endpoint reachable, the OTHER endpoint
// joins the frontier. Forward walks get the candidates; reverse walks get the
// group SOURCE, which is what keeps the may-relationship reportable from the far
// side. Frontier nodes are leaves — nothing is ever expanded from them.
func frontierOf(groups []CandidateGroup, reachable map[string]bool) map[string]bool {
	frontier := make(map[string]bool)
	for gi := range groups {
		for mi := range groups[gi].Members {
			m := &groups[gi].Members[mi]
			if reachable[m.FromId] {
				frontier[m.ToId] = true
			}
			if reachable[m.ToId] {
				frontier[m.FromId] = true
			}
		}
	}
	return frontier
}

// FrontierEdges keeps only the edges whose BOTH endpoints survived the frontier
// filter, so the edges section never shows a path continuing past a group the
// node list says the walk stopped at. Rendering both would let one response
// simultaneously deny a node is reachable and show an edge reaching it.
//
// This is the traverse arm's form of the dangling-source rule the graph-wide arm
// already applies: an emitter must not show edges the node enumeration says are
// not there.
//
// A NIL reached SET MEANS NO FRONTIER RAN (no groups), and the input is returned
// untouched — the caller gates on len(groups) > 0. Filtering on a nil set would
// drop every edge of every ordinary traversal in the product.
func FrontierEdges(start string, edges []knowledgev1.Edge, reached map[string]bool) []knowledgev1.Edge {
	if len(reached) == 0 {
		return edges
	}
	kept := make([]knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		fromOK := e.FromId == start || reached[e.FromId]
		toOK := e.ToId == start || reached[e.ToId]
		if fromOK && toOK {
			kept = append(kept, copyGroupEdge(e))
		}
	}
	return kept
}

// edgeIdent identifies one observed edge for set membership. Evidence is part of
// the identity because two references from the same declaration to the same
// target are distinct edges that differ only by their group key.
type edgeIdent struct {
	from     string
	to       string
	edgeType string
	evidence string
}

func identOf(e *knowledgev1.Edge) edgeIdent {
	return edgeIdent{from: e.FromId, to: e.ToId, edgeType: e.Type, evidence: e.Evidence}
}

// FrontierFilter implements the uniform short-circuit: A TRAVERSAL NEVER EXPANDS
// THROUGH A MULTI-CANDIDATE GROUP IN EITHER DIRECTION. The group is a frontier —
// the walk reports the ambiguity and stops there, and continuing is the reader's
// explicit act (re-traverse from a validated candidate).
//
// It is a SUBTRACTION over the server's flat result list, which is what makes the
// no-groups case provably unchanged: with no groups it returns the input slice
// itself, allocating nothing.
//
// THE RULE. Let G be the group member edges. R is the set of nodes reachable from
// start over the observed edges EXCLUDING G, following an edge from whichever
// endpoint the walk arrived at (matching the server's own both-union in
// CollectEdgesAlongWalk). C is the frontier: for any member edge of any group
// with either endpoint in R or equal to start, the OTHER endpoint joins C. C
// nodes are leaves and are never expanded from.
//
// C IS SYMMETRIC ON PURPOSE. On a forward walk this is exactly "the ToIds of
// groups whose source is reachable". On a REVERSE walk it is what keeps the group
// SOURCE — the walk arrives at a candidate and the source is only reachable
// across a group edge — so the may-relationship is still reported from the far
// side. Omitting it would understate impact/blast-radius by omission, which is
// the one thing the short-circuit ruling forbids: reporting the ambiguity is not
// omitting the edge.
//
// A RESULT NODE THE OBSERVED EDGES DO NOT EXPLAIN IS KEPT, NEVER DROPPED — its
// reachability is UNKNOWN, not disproven, and dropping it would let a truncated
// response render as a confident, smaller graph. Such a node sets incomplete.
//
// incomplete IS FALSE WHENEVER len(groups) == 0, unconditionally. Code-graph
// traversals now always request edge metadata, so a large ordinary walk with no
// groups can come back truncated; flagging "group reconstruction incomplete"
// there would describe a reconstruction that never happened. The caller ORs in
// the response's own truncation flag under the same len(groups) > 0 gate.
func FrontierFilter(start string, results []TraversalResult, edges []knowledgev1.Edge, groups []CandidateGroup) (kept []TraversalResult, reached map[string]bool, incomplete bool) {
	if len(groups) == 0 {
		return results, nil, false
	}

	adj, endpoints := boundAdjacency(edges, groups)
	reachable := reachableOver(start, adj)
	frontier := frontierOf(groups, reachable)

	reached = make(map[string]bool, len(reachable)+len(frontier))
	for id := range reachable {
		reached[id] = true
	}
	for id := range frontier {
		reached[id] = true
	}

	kept = make([]TraversalResult, 0, len(results))
	for _, r := range results {
		if r.Node == nil {
			continue
		}
		id := r.Node.Id
		if id == start || reachable[id] || frontier[id] {
			kept = append(kept, r) // Distance and order preserved verbatim.
			continue
		}
		if !endpoints[id] {
			// No incident edge in the carrier at all: the carrier is
			// incomplete, so this node's reachability is unknown. Keep it.
			kept = append(kept, r)
			incomplete = true
			continue
		}
		// Explained by the edge set, but only reachable through a group: this is
		// the node behind the frontier that the short-circuit stops at.
	}
	return kept, reached, incomplete
}
