// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

// Execute satisfies render.Executor — the carrier seam the repointed FetchNode /
// IterEdges / resolveAssembleByName / listPracticeGraphs use (T-GTB3 Phase 6 +
// T-GTB6). It resolves the target graph from the plan's GraphSelector (knowledge
// default, practice via language) and answers RETURN_MODE_GRAPH_NAMES (practice
// list), RETURN_MODE_EDGES, type-browse, and ByID from the fixture as the
// matching carrier (graph_names_json / edges_json / nodes_json).
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

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		var out []knowledgev1.Edge
		bucket := g.f.edges[k]
		for i := range bucket {
			e := &bucket[i]
			if e.FromId == q.GetById() || e.ToId == q.GetById() {
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

// handleQuery dispatches the three query shapes render/ uses:
//
//   - {graph:"practice"} with no id → list practice graphs.
//   - {id:..., include_edges:true} → return {node, edges:[]}.
//   - {id:...} → return the bare wire-node JSON.
//   - {type:...} (no id) → return matching nodes from knowledge.
func (g *fakeGcFixture) handleQuery(args json.RawMessage) kgtools.ToolResult {
	var req struct {
		ID           string `json:"id"`
		IncludeEdges bool   `json:"include_edges"`
		Graph        string `json:"graph"`
		Language     string `json:"language"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Format       string `json:"format"`
	}
	_ = json.Unmarshal(args, &req)

	// List-practice-graphs path: query({graph:"practice"}).
	if req.Graph == "practice" && req.ID == "" {
		names := make([]string, 0, len(g.f.practiceGraphs))
		for n := range g.f.practiceGraphs {
			names = append(names, n)
		}
		sort.Strings(names)
		type entry struct {
			Name string `json:"name"`
		}
		entries := make([]entry, len(names))
		for i, n := range names {
			entries[i] = entry{Name: n}
		}
		body, err := json.Marshal(struct {
			Graphs []entry `json:"graphs"`
		}{Graphs: entries})
		if err != nil {
			return kgtools.ErrorResult("marshal: " + err.Error())
		}
		return kgtools.TextResult(string(body))
	}

	// Determine target graph (knowledge default, practice via lang/name).
	graphType := req.Graph
	graphName := req.Name
	if graphType == "practice" && req.Language != "" {
		graphName = req.Language
	}
	k := graphKey(graphType, graphName)

	if req.IncludeEdges && req.ID != "" {
		// query(id:, include_edges:true) → {edges:[...]}.
		return g.renderEdgesResponse(k, req.ID)
	}

	if req.ID != "" {
		nodes := g.f.nodes[k]
		if nodes == nil {
			return kgtools.ErrorResult("not found")
		}
		n, ok := nodes[req.ID]
		if !ok {
			return kgtools.ErrorResult("not found")
		}
		return g.renderNodeJSON(n)
	}

	// query(type:) — only knowledge graph; return matching nodes as
	// a flat array of {id, symbol_name, type}.
	if req.Type != "" {
		knowledge := g.f.nodes[graphKey("", "")]
		type listRow struct {
			ID         string `json:"id"`
			SymbolName string `json:"symbol_name"`
			Type       string `json:"type"`
		}
		var rows []listRow
		for _, n := range knowledge {
			if n.Type != req.Type {
				continue
			}
			rows = append(rows, listRow{ID: n.Id, SymbolName: n.SymbolName, Type: n.Type})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		body, err := json.Marshal(rows)
		if err != nil {
			return kgtools.ErrorResult("marshal: " + err.Error())
		}
		return kgtools.TextResult(string(body))
	}

	return kgtools.ErrorResult("fakeGcFixture: empty query")
}

// renderNodeJSON marshals a wire node into the bare on-the-wire
// shape FetchNode parses. Fields are flat (id, type, symbol_name,
// description, summary, content, status, keywords, source) plus a
// metadata map for inline key/value pairs.
func (g *fakeGcFixture) renderNodeJSON(n *knowledgev1.Node) kgtools.ToolResult {
	type payload struct {
		ID          string            `json:"id"`
		Type        string            `json:"type"`
		SymbolName  string            `json:"symbol_name"`
		Description string            `json:"description"`
		Summary     string            `json:"summary"`
		Content     string            `json:"content"`
		Status      string            `json:"status"`
		Keywords    string            `json:"keywords"`
		Source      string            `json:"source"`
		Metadata    map[string]string `json:"metadata"`
	}
	p := payload{
		ID:          n.Id,
		Type:        n.Type,
		SymbolName:  n.SymbolName,
		Description: n.Description,
		Summary:     n.Summary,
		Content:     n.Content,
		Status:      n.Status,
		Keywords:    n.Keywords,
		Source:      n.Source,
		Metadata:    n.Metadata,
	}
	body, err := json.Marshal(p)
	if err != nil {
		return kgtools.ErrorResult("marshal: " + err.Error())
	}
	return kgtools.TextResult(string(body))
}

// renderEdgesResponse marshals the (outgoing + incoming) edges for
// nodeID in graph k into the {edges:[]} wire shape IterEdges parses.
func (g *fakeGcFixture) renderEdgesResponse(k, nodeID string) kgtools.ToolResult {
	type row struct {
		PeerID       string `json:"peer_id"`
		Relationship string `json:"relationship"`
		Direction    string `json:"direction"`
	}
	var rows []row
	bucket := g.f.edges[k]
	for i := range bucket {
		e := &bucket[i]
		if e.FromId == nodeID {
			rows = append(rows, row{
				PeerID: e.ToId, Relationship: e.Type, Direction: "outgoing",
			})
		}
		if e.ToId == nodeID {
			rows = append(rows, row{
				PeerID: e.FromId, Relationship: e.Type, Direction: "incoming",
			})
		}
	}
	body, err := json.Marshal(struct {
		Edges []row `json:"edges"`
	}{Edges: rows})
	if err != nil {
		return kgtools.ErrorResult("marshal: " + err.Error())
	}
	return kgtools.TextResult(string(body))
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
