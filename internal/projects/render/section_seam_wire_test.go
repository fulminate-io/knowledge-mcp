// SPDX-License-Identifier: Apache-2.0

package render_test

// section_seam_wire_test.go is the WIRE STAND-IN for the chunked-plan seam
// tests. It lives in the EXTERNAL test package because the seam's two sides are
// projects.BuildPlanGraph (the write) and render.Handle (the read), and package
// render cannot import projects — projects imports render.
//
// IT CARRIES EVERY EDGE FIELD, and that is load-bearing rather than tidy: a
// positioned child's `position` rides on the containment edge's Evidence, and
// engine.EdgesFromProto copies Evidence verbatim off the wire. A stand-in that
// rebuilt an edge from FromId/ToId/Type would silently strip the ordering key,
// and every assertion below would read as "position ignored" while passing
// against a correct implementation.

import (
	"context"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// wireFixture is an in-memory node/edge graph answering the four plan shapes the
// render arms issue: a by-id read, a bulk ids[] hydrate, a bounded edges read
// and a bounded-depth traversal.
type wireFixture struct {
	nodes map[string]*knowledgev1.Node
	edges []knowledgev1.Edge
}

func newWireFixture() *wireFixture {
	return &wireFixture{nodes: map[string]*knowledgev1.Node{}}
}

func (w *wireFixture) addNode(n *knowledgev1.Node) { w.nodes[n.Id] = n }

func (w *wireFixture) addEdge(from, to, edgeType, method, evidence string) {
	w.edges = append(w.edges, knowledgev1.Edge{
		FromId: from, ToId: to, Type: edgeType, Method: method, Evidence: evidence,
	})
}

// copyEdge reproduces an edge with EVERY field, the way the wire decode does.
func copyEdge(e *knowledgev1.Edge) knowledgev1.Edge {
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

// Execute answers the four plan shapes. EVERY ARM PRECEDES THE ByID FALLTHROUGH:
// a shape with no arm falls through to an empty ById and returns an empty
// response, which reads downstream as "the graph holds nothing" rather than as a
// fixture gap — that is how a missing traversal arm renders every tree childless.
func (w *wireFixture) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL:
		return w.traverse(q), nil
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return w.edgesFor(q.GetIds()), nil
	case len(q.GetIds()) > 0:
		return w.hydrate(q.GetIds()), nil
	case q.GetById() != "":
		if n, ok := w.nodes[q.GetById()]; ok {
			return enginetest.ResponseWithNodes(n), nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (w *wireFixture) hydrate(ids []string) *knowledgev1.ExecuteResponse {
	out := make([]*knowledgev1.Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := w.nodes[id]; ok {
			out = append(out, n)
		}
	}
	return enginetest.ResponseWithNodes(out...)
}

// edgesFor returns every edge touching one of the pivots, both directions, which
// is what an unset Forward means on the wire.
func (w *wireFixture) edgesFor(pivots []string) *knowledgev1.ExecuteResponse {
	set := map[string]struct{}{}
	for _, p := range pivots {
		set[p] = struct{}{}
	}
	var out []*knowledgev1.Edge
	for i := range w.edges {
		e := &w.edges[i]
		_, from := set[e.FromId]
		_, to := set[e.ToId]
		if !from && !to {
			continue
		}
		cp := copyEdge(e)
		out = append(out, &cp)
	}
	return &knowledgev1.ExecuteResponse{Edges: out}
}

// traverse walks OUTGOING edges of the admitted type from the plan's root,
// bounded by MaxHops, returning the descendants with their distances plus every
// edge walked — mirroring the server's traversal + edge collection.
func (w *wireFixture) traverse(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	root := ""
	if from := q.GetSelection().GetFromId(); len(from) > 0 {
		root = from[0]
	}
	admitted := map[string]bool{}
	for _, t := range q.GetSelection().GetEdgeTypes() {
		admitted[t] = true
	}
	maxHops := int(q.GetMaxHops())
	if maxHops == 0 {
		maxHops = 32
	}

	type frontier struct {
		id   string
		dist int
	}
	visited := map[string]bool{root: true}
	queue := []frontier{{root, 0}}
	var results []engine.TraversalResult
	var walked []*knowledgev1.Edge
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= maxHops {
			continue
		}
		for i := range w.edges {
			e := &w.edges[i]
			if e.FromId != cur.id {
				continue
			}
			if len(admitted) > 0 && !admitted[e.Type] {
				continue
			}
			cp := copyEdge(e)
			walked = append(walked, &cp)
			if visited[e.ToId] {
				continue
			}
			visited[e.ToId] = true
			if n, ok := w.nodes[e.ToId]; ok {
				results = append(results, engine.TraversalResult{Node: n, Distance: cur.dist + 1})
			}
			queue = append(queue, frontier{e.ToId, cur.dist + 1})
		}
	}
	resp := traversalResponse(results)
	if q.GetIncludeEdgeMetadata() {
		resp.TraversalEdges = walked
	}
	return resp
}

// traversalResponse packs decoded results back into the wire carrier the
// traversal decoder reads.
func traversalResponse(results []engine.TraversalResult) *knowledgev1.ExecuteResponse {
	out := make([]*knowledgev1.TraversalResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.TraversalResult{Node: r.Node, Distance: int32(r.Distance)}
	}
	return &knowledgev1.ExecuteResponse{TraversalResults: out}
}

// sortedSectionNames is a small helper the seam tests share.
func sortedSectionNames(nodes map[string]*knowledgev1.Node) []string {
	var out []string
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodePlanSection {
			out = append(out, n.SymbolName)
		}
	}
	sort.Strings(out)
	return out
}
