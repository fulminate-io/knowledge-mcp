// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// edgeDirection is the client-side mirror of store.EdgeDirection: the recipe
// interpreter traverses edges in / out / both directions and the store enum is
// server-internal, so the recipe package carries its own three-value enum. The
// values are referenced only within this package (evalTraverse / has_edge /
// ancestors / children walks).
type edgeDirection int

const (
	outgoingEdges edgeDirection = iota
	incomingEdges
	bothEdges
)

// sourceView is the in-memory read layer the relocated interpreter reads
// through instead of a store.DB. It is the recipe analog of
// foundation.GonumGraph: the whole source graph is materialized ONCE — via two
// BOUNDED PAGED DRAINS (FetchAllNodes + FetchAllEdges) — into four indexes,
// and every eval* read (select-by-type, by-id hydrate, edge traversal) hits a
// map, never the wire. This avoids the per-row IterEdges N+1 Execute storm the
// server interpreter could afford against an in-process store but the client
// cannot afford over the wire.
//
// All four indexes share the same *knowledgev1.Node / knowledgev1.Edge values
// FetchAllNodes / FetchEdges decoded; the four indexes are read-only after load,
// and the two memo fields below (censusCached and docOrder) are lazily-built
// CACHES over them — each computed at most once per run, from the indexes, and
// never writing back to them.
type sourceView struct {
	// byID maps node ID → the hydrated node. Powers nodeByID (the by-id
	// hydrate the server did via store.ByID).
	byID map[string]*knowledgev1.Node
	// byType maps node Type → the nodes of that type, in browse order.
	// Powers nodesByType (the select via store.Match(NodeType)).
	byType map[string][]*knowledgev1.Node
	// outEdges / inEdges map a node ID → its outgoing / incoming edges.
	// Powers edgesFrom (the traversal via store.IterEdges). An edge appears
	// in outEdges[FromId] and inEdges[ToId]. Edges are stored by POINTER:
	// knowledgev1.Edge embeds a proto MessageState (a sync.Mutex), so copying
	// one by value trips copylocks — the index always holds *knowledgev1.Edge.
	outEdges map[string][]*knowledgev1.Edge
	inEdges  map[string][]*knowledgev1.Edge

	// graphType / name are the identity of the graph the four indexes were drawn
	// from. Every refusal validate_source.go raises names the graph it checked
	// against as "<graphType>/<name>", so the vocabulary and the identity of its
	// source travel together on one value and cannot drift apart. Keeping them
	// here rather than on Interpret's signature is also what leaves that
	// signature — and its direct callers — untouched.
	graphType kgtypes.GraphType
	name      string

	// censusOnce / censusCached memoize the vocabulary census; censusWalks counts
	// how many times the graph was actually WALKED to build it. The counter is
	// incremented inside buildCensus, never in census(), so "computed once per
	// run" is a measurement rather than a restatement of what sync.Once
	// guarantees structurally.
	censusOnce   sync.Once
	censusCached *sourceCensus
	censusWalks  int

	// docOrder memoizes the per-run reading-order index (document_order.go).
	// Built lazily on first use rather than in loadSourceView, because this
	// package's tests construct sourceView literals directly and an eager build
	// would leave every such fixture with a nil index.
	docOrder *documentOrder
}

// loadSourceView materializes the source graph identified by (graphType, name)
// into an in-memory sourceView using two BOUNDED PAGED DRAINS, mirroring
// foundation.newGonumGraph's materializeNodes + materializeEdges:
//
//  1. FetchAllNodes  — an id-keyset browse drain over every node, one bounded
//     page per Execute.
//  2. FetchAllEdges  — a pivot-page edge drain over the loaded id set, one
//     bounded page of pivots per Execute.
//
// The RPC count therefore scales with the graph rather than being fixed at two:
// ceil(nodes/paging.BrowsePageSize) + ceil(ids/paging.EdgePivotPageSize)
// sequential round trips. What still holds — and is the load-bearing
// N+1-avoidance property the client migration depends on — is that NO PER-ROW
// Execute is issued during this load or during any subsequent interpretation
// read: the round trips are per PAGE, never per node or per edge.
func loadSourceView(
	ctx context.Context,
	caller foundation.GraphCaller,
	graphType kgtypes.GraphType,
	name string,
) (*sourceView, error) {
	if caller == nil {
		return nil, fmt.Errorf("recipe: loadSourceView: graph caller unavailable")
	}
	nodes, err := foundation.FetchAllNodes(ctx, caller, graphType, name)
	if err != nil {
		return nil, fmt.Errorf("recipe: load source nodes %s/%s: %w", graphType, name, err)
	}

	sv := &sourceView{
		byID:      make(map[string]*knowledgev1.Node, len(nodes)),
		byType:    make(map[string][]*knowledgev1.Node),
		outEdges:  make(map[string][]*knowledgev1.Edge),
		inEdges:   make(map[string][]*knowledgev1.Edge),
		graphType: graphType,
		name:      name,
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		sv.byID[n.Id] = n
		sv.byType[n.Type] = append(sv.byType[n.Type], n)
	}
	if len(sv.byID) == 0 {
		return sv, nil
	}

	// Whole-graph: this view indexes EVERY node of the source graph
	// unconditionally, so the edge read drives off every indexed id as the pivot
	// set, read in bounded pages. The maps below are read by keyed lookup
	// (edgesFrom / edgesTo), never ranged, so an entry keyed on an id no node
	// carries is unreachable rather than visible.
	ids := make([]string, 0, len(sv.byID))
	for id := range sv.byID {
		ids = append(ids, id)
	}
	edges, err := foundation.FetchAllEdges(ctx, caller, graphType, name, ids, nil)
	if err != nil {
		return nil, fmt.Errorf("recipe: load source edges %s/%s: %w", graphType, name, err)
	}
	for i := range edges {
		e := &edges[i]
		sv.outEdges[e.FromId] = append(sv.outEdges[e.FromId], e)
		sv.inEdges[e.ToId] = append(sv.inEdges[e.ToId], e)
	}
	return sv, nil
}

// nodesByType returns every node of the given type in browse order, mirroring
// the server interpreter's sourceDB.Query(store.Match(store.NodeType(t))). An
// unknown type returns nil (zero rows) — same fail-soft shape evalSelect relied
// on. The returned slice aliases the index; callers must not mutate it.
func (sv *sourceView) nodesByType(nodeType string) []*knowledgev1.Node {
	return sv.byType[nodeType]
}

// nodeByID returns the node with the given ID and whether it was found,
// mirroring the server interpreter's hydrateNode via sourceDB.Query(store.ByID).
// A miss is (nil, false) so evalTraverse / the *_concat builtins can skip orphan
// edges exactly as they did against the store.
func (sv *sourceView) nodeByID(id string) (*knowledgev1.Node, bool) {
	n, ok := sv.byID[id]
	return n, ok
}

// edgesFrom returns the neighbor node IDs reachable from id along edges of
// edgeType in the given direction, mirroring the server interpreter's
// sourceDB.IterEdges(EdgeIterRequest{NodeID, Direction, EdgeTypes}). For
// outgoing edges the neighbor is the edge's ToId; for incoming, the FromId;
// bothEdges unions both (an out-edge contributes its ToId, an in-edge its
// FromId). Edges whose type does not match edgeType are skipped. Order follows
// the materialized edge order (out-edges before in-edges for bothEdges),
// matching the deterministic iteration the interpreter depends on.
func (sv *sourceView) edgesFrom(id, edgeType string, dir edgeDirection) []string {
	var out []string
	if dir == outgoingEdges || dir == bothEdges {
		for _, e := range sv.outEdges[id] {
			if e.Type == edgeType {
				out = append(out, e.ToId)
			}
		}
	}
	if dir == incomingEdges || dir == bothEdges {
		for _, e := range sv.inEdges[id] {
			if e.Type == edgeType {
				out = append(out, e.FromId)
			}
		}
	}
	return out
}

// neighborEdge pairs a reachable neighbor's node ID with the edge that reached
// it, so a walker can build a row that knows which edge produced it.
type neighborEdge struct {
	NodeID string
	Edge   *knowledgev1.Edge
}

// edgesAlong is edgesFrom with the edge KEPT rather than projected away.
//
// ITS DIRECTION ARMS MIRROR edgesFrom's EXACTLY — out-edges contribute ToId,
// in-edges contribute FromId, bothEdges unions in that order — so a rowset built
// through either walker visits the same neighbors in the same order. A
// divergence here would make `edge.…` readable over a row set that differs from
// the one every other builtin walks.
//
// IT SITS BESIDE edgesFrom RATHER THAN REPLACING IT, for the reason
// childEdgesOrdered gives below for the same choice: edgesFrom's four remaining
// callers want IDs and should not start allocating a struct per neighbor.
// edgesFrom is left exactly as it was.
func (sv *sourceView) edgesAlong(id, edgeType string, dir edgeDirection) []neighborEdge {
	var out []neighborEdge
	if dir == outgoingEdges || dir == bothEdges {
		for _, e := range sv.outEdges[id] {
			if e.Type == edgeType {
				out = append(out, neighborEdge{NodeID: e.ToId, Edge: e})
			}
		}
	}
	if dir == incomingEdges || dir == bothEdges {
		for _, e := range sv.inEdges[id] {
			if e.Type == edgeType {
				out = append(out, neighborEdge{NodeID: e.FromId, Edge: e})
			}
		}
	}
	return out
}

// childEdgesOrdered returns the outgoing edges of the given type from id, in
// DOCUMENT ORDER rather than materialization order.
//
// It exists beside edgesFrom rather than replacing it because edgesFrom
// projects every edge down to a neighbor ID, which puts the edge's own
// Evidence — where the position lives — structurally out of reach. edgesFrom is
// left exactly as it was: the concat builtins, the ancestor check and the
// traversal rule all depend on its current behaviour.
//
// THE POSITION IS READ NODE FIRST AND EDGE SECOND, and both carriers are real:
// the pdf emitter stamps `position` on every chunk node and the web emitter on
// every section node, while both stamp one on every contains EDGE they emit. The
// child node's own key wins when it parses as an integer; otherwise the key
// falls back to the edge's Evidence. On a graph collected by either raw
// collector the two agree, so the precedence is observable only where they
// disagree or one is absent.
//
// ORDERING RULE: ascending by that key; an edge yielding no key sorts AFTER
// every keyed edge and keeps materialization order among its unkeyed peers. The
// sort is stable, so a fixed source graph renders in a fixed order across runs —
// which matters because extract output is read by people who compare one run
// against another. THE COMPARATOR ITSELF LIVES IN document_order.go as
// sortEdgesByOrderKey, shared with the reading-order index so the two cannot
// order the same children differently.
func (sv *sourceView) childEdgesOrdered(id, edgeType string) []*knowledgev1.Edge {
	all := sv.outEdges[id]
	out := make([]*knowledgev1.Edge, 0, len(all))
	for _, e := range all {
		if e.Type == edgeType {
			out = append(out, e)
		}
	}
	sv.sortEdgesByOrderKey(out)
	return out
}
