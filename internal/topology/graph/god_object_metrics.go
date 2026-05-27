// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// god_object_metrics.go holds the four Chidamber-Kemerer style metric
// computers used by GodObjectAnalyzer plus the bulk-edge fetch that feeds
// them. The prior store-backed implementation issued a per-candidate
// IterEdges fan-out (one query per candidate per metric); this version
// fetches the candidate set's edges in ONE bulk wire read and the contained
// methods' callee edges in a SECOND bulk read, then groups the edges per
// node in memory. The CK metric definitions are byte-for-byte unchanged —
// only the data-access layer collapses from N+1 to two bulk reads.
//
// Topology is architecturally forbidden from importing the chunker or any
// domain package, so the helpers know nothing about tree-sitter and operate
// purely on raw EdgeType / NodeType strings.

// computeGodObjectRows runs the four CK metrics against every candidate and
// returns one godObjectMetrics row per node. It performs exactly two bulk
// wire fetches: one over the candidate IDs (covering CBO, WMC, FanIn, and
// the contained-method discovery for RFC) and one over the union of every
// candidate's contained methods (covering RFC's one-hop callee walk). Errors
// short-circuit the pass — the analyzer either produces a complete dataset
// or no findings, because partial data would corrupt the percentile
// distribution.
func computeGodObjectRows(ctx context.Context, req foundation.Request, candidates []string) ([]godObjectMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/god_object: %w", err)
	}

	// Bulk read 1: every edge incident to the candidate set.
	candEdges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, candidates, nil)
	if err != nil {
		return nil, fmt.Errorf("topology/god_object: candidate edges: %w", err)
	}
	candSet := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		candSet[id] = struct{}{}
	}

	// Contained methods per candidate (forward CONTAINS, parent → child),
	// deduped + self/empty filtered — identical to the prior
	// containedMethodIDs helper.
	methodsByCand := containedMethodsFromEdges(candEdges, candSet)

	// Bulk read 2: every CALLS edge incident to the union of contained
	// methods — RFC's one-hop callee walk.
	methodSet := make(map[string]struct{})
	var methodIDs []string
	for _, id := range candidates {
		for _, m := range methodsByCand[id] {
			if _, dup := methodSet[m]; dup {
				continue
			}
			methodSet[m] = struct{}{}
			methodIDs = append(methodIDs, m)
		}
	}
	methodCallEdges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, methodIDs, []kgtypes.EdgeType{kgtypes.EdgeCalls})
	if err != nil {
		return nil, fmt.Errorf("topology/god_object: method callee edges: %w", err)
	}
	calleesByMethod := outgoingCalleesByNode(methodCallEdges, methodSet)

	rows := make([]godObjectMetrics, 0, len(candidates))
	for _, id := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/god_object: %w", err)
		}
		methods := methodsByCand[id]
		rows = append(rows, godObjectMetrics{
			StringID: id,
			CBO:      computeCBO(candEdges, id),
			RFC:      computeRFC(methods, calleesByMethod),
			WMC:      len(methods),
			FanIn:    computeFanIn(candEdges, id),
		})
	}
	return rows, nil
}

// containedMethodsFromEdges extracts, for each candidate, the deduplicated
// set of node IDs reached via forward CONTAINS edges (parent → child),
// excluding self-references and empty IDs. Mirrors the prior
// containedMethodIDs helper applied to the bulk candidate-edge read.
func containedMethodsFromEdges(edges []knowledgev1.Edge, candSet map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(candSet))
	seen := make(map[string]map[string]bool, len(candSet))
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
			continue
		}
		if _, ok := candSet[e.FromId]; !ok {
			continue
		}
		child := e.ToId
		if child == "" || child == e.FromId {
			continue
		}
		dedup, ok := seen[e.FromId]
		if !ok {
			dedup = make(map[string]bool)
			seen[e.FromId] = dedup
		}
		if dedup[child] {
			continue
		}
		dedup[child] = true
		out[e.FromId] = append(out[e.FromId], child)
	}
	return out
}

// outgoingCalleesByNode groups the ToId of every outgoing CALLS edge by its
// FromId, restricted to source nodes in nodeSet. Empty ToIds are dropped to
// match the prior RFC callee filter.
func outgoingCalleesByNode(edges []knowledgev1.Edge, nodeSet map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(nodeSet))
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		if _, ok := nodeSet[e.FromId]; !ok {
			continue
		}
		if e.ToId == "" {
			continue
		}
		out[e.FromId] = append(out[e.FromId], e.ToId)
	}
	return out
}

// computeCBO returns the Coupling Between Objects metric for a single
// type-ish node: the number of distinct OTHER targets reached via outgoing
// USES_TYPE or CALLS edges from this type. Self-edges are excluded so a type
// that references itself doesn't inflate its own CBO. Method-level callees
// are NOT walked here — those belong to RFC (one-hop response set), not CBO.
func computeCBO(edges []knowledgev1.Edge, stringID string) int {
	if stringID == "" {
		return 0
	}
	seen := make(map[string]bool)
	for i := range edges {
		e := &edges[i]
		if e.FromId != stringID {
			continue
		}
		et := kgtypes.EdgeType(e.Type)
		if et != kgtypes.EdgeUsesType && et != kgtypes.EdgeCalls {
			continue
		}
		if e.ToId == "" || e.ToId == stringID {
			continue
		}
		seen[e.ToId] = true
	}
	return len(seen)
}

// computeRFC returns the Response For Class metric for a single type-ish
// node: the size of the union of (a) the methods owned by this type
// (CONTAINS forward edges from the type) and (b) the unique direct callees
// of those methods (CALLS forward edges from each method, one-hop only).
//
// One hop is the strict CK definition: going deeper introduces unbounded
// noise without improving the god-object signal because every method
// transitively reaches the standard library.
func computeRFC(methods []string, calleesByMethod map[string][]string) int {
	response := make(map[string]bool, len(methods))
	for _, m := range methods {
		response[m] = true
	}
	for _, m := range methods {
		for _, callee := range calleesByMethod[m] {
			response[callee] = true
		}
	}
	return len(response)
}

// computeFanIn returns the structural fan-in for a single type-ish node:
// the count of incoming CALLS, USES_TYPE, and CONTAINS edges. This is more
// selective than the generic FanInAnalyzer (which counts every incoming
// edge type regardless of meaning) because god-object fan-in is
// specifically about "who structurally depends on me" — pollution from
// documentation or relates-to edges would dilute the signal.
//
// Like the prior IterEdges tally, this is a raw edge COUNT (every matching
// incoming edge counts), not a distinct-source count.
func computeFanIn(edges []knowledgev1.Edge, stringID string) int {
	if stringID == "" {
		return 0
	}
	n := 0
	for i := range edges {
		e := &edges[i]
		if e.ToId != stringID {
			continue
		}
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeCalls, kgtypes.EdgeUsesType, kgtypes.EdgeContains:
			n++
		}
	}
	return n
}
