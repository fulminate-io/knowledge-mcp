// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graphFixture is an in-memory node-and-edge graph used as the
// backing store for fakeGc. Each test owns one fixture and seeds
// nodes + edges into it. The fixture answers wire-shape gc.Call
// requests (`query(id:)`, `query(id:, include_edges:true)`,
// `query(graph:practice)`, `query(type:)`) for the surface render/
// exercises: FetchNode, IterEdges, listPracticeGraphs,
// resolveAssembleByName.
//
// Practice-graph scoping: nodes can be tagged with a `graphType`
// + `graphName` pair on insertion. The default graph is
// knowledge/"" (matching the FetchNode default).
type graphFixture struct {
	// keyed by ("graphType:graphName") → id → node.
	nodes map[string]map[string]*knowledgev1.Node
	// keyed by ("graphType:graphName") → []edge.
	edges map[string][]knowledgev1.Edge
	// Track edge buckets so we can answer query(graph:practice) with
	// the loaded set of practice-graph names.
	practiceGraphs map[string]bool
}

func newGraphFixture() *graphFixture {
	return &graphFixture{
		nodes:          map[string]map[string]*knowledgev1.Node{},
		edges:          map[string][]knowledgev1.Edge{},
		practiceGraphs: map[string]bool{},
	}
}

// graphKey is the internal map key for a (graphType, graphName) pair.
func graphKey(graphType, graphName string) string {
	if graphType == "" {
		graphType = "knowledge"
	}
	return graphType + ":" + graphName
}

// addNode inserts n into the fixture under the given graph. Empty
// graphType selects the default knowledge graph.
func (f *graphFixture) addNode(graphType, graphName string, n *knowledgev1.Node) *graphFixture {
	k := graphKey(graphType, graphName)
	if f.nodes[k] == nil {
		f.nodes[k] = map[string]*knowledgev1.Node{}
	}
	f.nodes[k][n.Id] = n
	if graphType == "practice" && graphName != "" {
		f.practiceGraphs[graphName] = true
	}
	return f
}

// addKnowledgeNode is the common case helper.
func (f *graphFixture) addKnowledgeNode(n *knowledgev1.Node) *graphFixture {
	return f.addNode("", "", n)
}

// addEdge inserts a fresh copy of e into the fixture under the given graph.
// e is a pointer because knowledgev1.Edge value-embeds the proto MessageState
// (copylocks forbids passing/copying it by value); the body reconstructs a
// fresh literal into the slice.
func (f *graphFixture) addEdge(graphType, graphName string, e *knowledgev1.Edge) *graphFixture {
	k := graphKey(graphType, graphName)
	f.edges[k] = append(f.edges[k], knowledgev1.Edge{
		FromId:        e.FromId,
		ToId:          e.ToId,
		Type:          e.Type,
		Weight:        e.Weight,
		Confidence:    e.Confidence,
		Method:        e.Method,
		Evidence:      e.Evidence,
		LastValidated: e.LastValidated,
	})
	if graphType == "practice" && graphName != "" {
		f.practiceGraphs[graphName] = true
	}
	return f
}

// addKnowledgeEdge is the common case helper for edges in the
// knowledge graph.
func (f *graphFixture) addKnowledgeEdge(from, to string, et kgtypes.EdgeType) *graphFixture {
	return f.addEdge("", "", &knowledgev1.Edge{FromId: from, ToId: to, Type: string(et)})
}

// link is a chainable shortcut that creates an EdgeKGContains
// edge in the knowledge graph (the most common case in the moved
// tests).
func (f *graphFixture) link(from, to string) *graphFixture {
	return f.addKnowledgeEdge(from, to, kgtypes.EdgeKGContains)
}

// gc returns a GraphCaller backed by the fixture.
func (f *graphFixture) gc() GraphCaller {
	return &fakeGcFixture{f: f}
}

// traversalResponseFor answers a RETURN_MODE_TRAVERSAL plan out of an in-memory
// fixture: a BFS from the plan's root, bounded by MaxHops (0 meaning
// unbounded), following only OUTGOING edges whose type the plan's
// Selection.EdgeTypes admits, and returning the descendant nodes (root
// excluded) with their distances plus — when IncludeEdgeMetadata is set — every
// edge walked. This mirrors the server's traversal + CollectEdgesAlongWalk.
//
// TWO closures rather than one, because the walk needs both halves: edgesFor
// finds the children of an id, and nodeFor resolves each child to the node the
// TraversalResult carries. The two fixtures in this package key their storage
// differently — scriptedGc holds edges and nodes per node id, graphFixture holds
// them per graph bucket and filters by FromId itself — so the closures are the
// seam that lets one BFS serve both.
//
// An empty Selection.EdgeTypes admits every edge type, matching the wire
// contract that an unset edge-type filter is not a filter.
func traversalResponseFor(
	q *knowledgev1.QueryPlan,
	edgesFor func(id string) []knowledgev1.Edge,
	nodeFor func(id string) *knowledgev1.Node,
) *knowledgev1.ExecuteResponse {
	root := q.GetSelection().GetFromId()[0]
	maxHops := int(q.GetMaxHops())
	if maxHops <= 0 {
		maxHops = 1 << 30
	}
	wantType := map[string]bool{}
	for _, et := range q.GetSelection().GetEdgeTypes() {
		wantType[et] = true
	}
	admits := func(t string) bool { return len(wantType) == 0 || wantType[t] }

	var results []engine.TraversalResult
	var walkedEdges []knowledgev1.Edge
	visited := map[string]bool{root: true}
	type frontier struct {
		id   string
		dist int
	}
	queue := []frontier{{root, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= maxHops {
			continue
		}
		curEdges := edgesFor(cur.id)
		for i := range curEdges {
			e := &curEdges[i]
			if e.FromId != cur.id || !admits(e.Type) {
				continue // outgoing, admitted-type edges only
			}
			walkedEdges = append(walkedEdges, knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
			if visited[e.ToId] {
				continue
			}
			visited[e.ToId] = true
			if n := nodeFor(e.ToId); n != nil {
				results = append(results, engine.TraversalResult{Node: n, Distance: cur.dist + 1})
			}
			queue = append(queue, frontier{e.ToId, cur.dist + 1})
		}
	}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForRenderTest(results)}
	if q.GetIncludeEdgeMetadata() {
		resp.TraversalEdges = edgesToProtoForTest(walkedEdges)
	}
	return resp
}

// fakeGcFixture is the GraphCaller adapter that routes gc.Call
// invocations into the in-memory fixture's lookups.
type fakeGcFixture struct {
	f *graphFixture
}

func (g *fakeGcFixture) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	switch tool {
	case "query":
		return g.handleQuery(args), nil
	case "mutate":
		// No-op: the moved tests don't exercise mutate calls. Render
		// surfaces that invoke gc.Call("mutate", ...) (test_plan
		// newRun=true) are not used by the moved progress/ticket
		// tests. Return an empty success to avoid surprising
		// failures if a renderer adds a stray call.
		return kgtools.TextResult(`{"ids":[]}`), nil
	}
	return kgtools.ErrorResult("fakeGcFixture: unexpected tool: " + tool), nil
}

// Execute satisfies render.Executor — the carrier seam every wire-fetch helper
// takes: FetchNode / FetchNodeIn, FetchNodesByIDs / FetchNodesByIDsIn,
// IterEdges / IterEdgesIn / IterEdgesFor, TraverseDescendantsWithEdges,
// resolveAssembleByName and listPracticeGraphs. It resolves the target graph
// from the plan's GraphSelector (knowledge default, practice via language) and
// answers RETURN_MODE_GRAPH_NAMES (practice list), RETURN_MODE_TRAVERSAL,
// RETURN_MODE_EDGES, a bulk Ids[] hydrate, a type-browse and a single ByID from
// the fixture as the matching carrier.
//
// EVERY ARM MUST PRECEDE THE ByID FALLTHROUGH. A plan shape with no arm falls
// through to ByID, finds it empty, and returns an EMPTY response — which reads
// downstream as "the graph holds nothing" rather than as a fixture gap. That is
// how a missing traversal arm renders every tree childless and a missing bulk
// arm renders every linked-node section empty.
func (g *fakeGcFixture) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	sel := req.GetTarget()
	graphType := sel.GetGraph()
	graphName := sel.GetName()
	if graphType == "practice" && sel.GetLanguage() != "" {
		graphName = sel.GetLanguage()
	}

	// listPracticeGraphs: query(graph:practice, mode:modules) → graph_names_json.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		names := make([]string, 0, len(g.f.practiceGraphs))
		for n := range g.f.practiceGraphs {
			names = append(names, n)
		}
		sort.Strings(names)
		infos := make([]*knowledgev1.GraphInfo, len(names))
		for i, n := range names {
			infos[i] = &knowledgev1.GraphInfo{Name: n}
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}

	k := graphKey(graphType, graphName)

	// The subtree traversal every batched render arm opens with. Placed BEFORE
	// the ById fallthrough: without this arm a traversal plan matches nothing,
	// falls through with an empty ById, and returns an empty response — which
	// renders every tree as a bare root line with no descendants.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		return traversalResponseFor(q,
			func(id string) []knowledgev1.Edge {
				var out []knowledgev1.Edge
				bucket := g.f.edges[k]
				for i := range bucket {
					e := &bucket[i]
					if e.FromId == id {
						out = append(out, knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
					}
				}
				return out
			},
			func(id string) *knowledgev1.Node { return g.f.nodes[k][id] },
		), nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// The pivot arrives either as the node-SET form (Ids[], what the paged
		// pivot drain sends) or as the single-node form (ById). Both select the
		// edges incident to any pivot, matching the server's node-SET carrier.
		pivots := map[string]bool{}
		for _, id := range q.GetIds() {
			pivots[id] = true
		}
		if id := q.GetById(); id != "" {
			pivots[id] = true
		}
		var out []knowledgev1.Edge
		bucket := g.f.edges[k]
		for i := range bucket {
			e := &bucket[i]
			if pivots[e.FromId] || pivots[e.ToId] {
				// Fresh literal (copylocks forbids copying an existing
				// knowledgev1.Edge value into the slice).
				out = append(out, knowledgev1.Edge{
					FromId:        e.FromId,
					ToId:          e.ToId,
					Type:          e.Type,
					Weight:        e.Weight,
					Confidence:    e.Confidence,
					Method:        e.Method,
					Evidence:      e.Evidence,
					LastValidated: e.LastValidated,
				})
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(out)}, nil
	}

	// The bulk hydrate every arm now opens its linked-node sections with: a
	// by-id read carrying an Ids[] SET rather than a single ById. Placed before
	// the ById fallthrough for the same reason the traversal arm is — without
	// it an Ids[] plan matches nothing, falls through with an empty ById, and
	// returns an empty response, so every linked-node section renders empty.
	if ids := q.GetIds(); len(ids) > 0 && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_UNSPECIFIED {
		out := make([]*knowledgev1.Node, 0, len(ids))
		for _, id := range ids {
			if n, ok := g.f.nodes[k][id]; ok {
				out = append(out, n)
			}
		}
		return enginetest.ResponseWithNodes(out...), nil
	}

	// resolveAssembleByName: a knowledge type-browse (Selection.NodeType, no
	// ById) → typed Nodes carrier of the matching knowledge nodes.
	if q.GetById() == "" && q.GetSelection().GetNodeType() != "" {
		typ := q.GetSelection().GetNodeType()
		var out []*knowledgev1.Node
		for _, n := range g.f.nodes[graphKey("", "")] {
			if n.Type == typ {
				out = append(out, n)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
		return enginetest.ResponseWithNodes(out...), nil
	}

	if nodes := g.f.nodes[k]; nodes != nil {
		if n, ok := nodes[q.GetById()]; ok {
			return enginetest.ResponseWithNode(n), nil
		}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// callRender runs a render entry point with the fixture's gc and
// returns the rendered text.
func callRender(ctx context.Context, f *graphFixture, args map[string]any) (string, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	res := Handle(ctx, f.gc(), raw)
	if res.IsError {
		return resultTextRender(res), nil
	}
	return resultTextRender(res), nil
}

// resultTextRender extracts text from a kgtools.ToolResult.
func resultTextRender(r kgtools.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
