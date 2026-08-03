// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"

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
// foundation.GonumGraph: the whole source graph is materialized ONCE — via
// exactly two Execute RPCs (FetchAllNodes + FetchEdges) — into four indexes,
// and every eval* read (select-by-type, by-id hydrate, edge traversal) hits a
// map, never the wire. This avoids the per-row IterEdges N+1 Execute storm the
// server interpreter could afford against an in-process store but the client
// cannot afford over the wire.
//
// All four indexes share the same *knowledgev1.Node / knowledgev1.Edge values
// FetchAllNodes / FetchEdges decoded; the view is read-only after load.
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
}

// loadSourceView materializes the source graph identified by (graphType, name)
// into an in-memory sourceView using EXACTLY two Execute RPCs, mirroring
// foundation.newGonumGraph's materializeNodes + materializeEdges:
//
//  1. FetchAllNodes  — one browse Execute returning every node.
//  2. FetchAllEdges  — one match-all RETURN_MODE_EDGES Execute (no pivot).
//
// No per-row Execute is issued during this load or during any subsequent
// interpretation read; that is the load-bearing N+1-avoidance property the
// client migration depends on.
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
		byID:     make(map[string]*knowledgev1.Node, len(nodes)),
		byType:   make(map[string][]*knowledgev1.Node),
		outEdges: make(map[string][]*knowledgev1.Edge),
		inEdges:  make(map[string][]*knowledgev1.Edge),
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
