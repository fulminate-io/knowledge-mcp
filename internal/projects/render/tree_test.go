// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// scriptedGc is the carrier-backed tree-render fake. It stores
// wire-node + knowledgev1.Edge fixtures per id and answers via Execute, returning the
// typed Nodes / Edges carriers the repointed FetchNode / IterEdges decode.
type scriptedGc struct {
	nodes map[string]*knowledgev1.Node  // id → node
	edges map[string][]knowledgev1.Edge // id → that node's edges (both directions)
	calls int
}

func newScriptedGc() *scriptedGc {
	return &scriptedGc{
		nodes: map[string]*knowledgev1.Node{},
		edges: map[string][]knowledgev1.Edge{},
	}
}

func (s *scriptedGc) putNode(id, name, ntype, status, desc string) *scriptedGc {
	s.nodes[id] = &knowledgev1.Node{
		Id: id, SymbolName: name, Type: ntype, Status: status, Description: desc,
	}
	return s
}

// putEdges converts the {peer_id, relationship, direction} rows into knowledgev1.Edge
// values (the carrier shape): outgoing → From=fromID,To=peer; incoming →
// From=peer,To=fromID.
func (s *scriptedGc) putEdges(fromID string, rows ...map[string]string) *scriptedGc {
	edges := make([]knowledgev1.Edge, len(rows))
	for i, r := range rows {
		fromN, toN := fromID, r["peer_id"]
		if r["direction"] == "incoming" {
			fromN, toN = r["peer_id"], fromID
		}
		// Assign a fresh literal into the slot (copylocks forbids copying an
		// existing knowledgev1.Edge value).
		edges[i] = knowledgev1.Edge{Type: r["relationship"], FromId: fromN, ToId: toN}
	}
	s.edges[fromID] = edges
	return s
}

func (s *scriptedGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (s *scriptedGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	s.calls++
	q := req.GetQuery()

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		return s.answerTraversal(q), nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// node-SET form (Ids[]) → union of outgoing edges among the id set,
		// filtered by Selection.EdgeTypes (the depends-on batch). Per-node
		// form (ById) → that node's edges.
		if ids := q.GetIds(); len(ids) > 0 {
			return &knowledgev1.ExecuteResponse{Edges: s.nodeSetEdges(ids, q.GetSelection().GetEdgeTypes())}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(s.edges[q.GetById()])}, nil
	}
	if n, ok := s.nodes[q.GetById()]; ok {
		return enginetest.ResponseWithNode(n), nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// answerTraversal computes the contains-descendant set of the root over the
// per-node edge fixtures (BFS up to MaxHops), returning the descendant nodes
// (root excluded) and — when IncludeEdgeMetadata is set — the contains edges
// among them, mirroring the server's traversal + CollectEdgesAlongWalk.
func (s *scriptedGc) answerTraversal(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	root := q.GetSelection().GetFromId()[0]
	maxHops := int(q.GetMaxHops())
	if maxHops <= 0 {
		maxHops = 1 << 30
	}
	var results []engine.TraversalResult
	var containsEdges []knowledgev1.Edge
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
		curEdges := s.edges[cur.id]
		for i := range curEdges {
			e := &curEdges[i]
			if e.FromId != cur.id || e.Type != string(kgtypes.EdgeKGContains) {
				continue // outgoing contains edges only
			}
			containsEdges = append(containsEdges, knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
			if visited[e.ToId] {
				continue
			}
			visited[e.ToId] = true
			if n, ok := s.nodes[e.ToId]; ok {
				results = append(results, engine.TraversalResult{Node: n, Distance: cur.dist + 1})
			}
			queue = append(queue, frontier{e.ToId, cur.dist + 1})
		}
	}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForRenderTest(results)}
	if q.GetIncludeEdgeMetadata() {
		resp.TraversalEdges = edgesToProtoForTest(containsEdges)
	}
	return resp
}

// nodeSetEdges unions every pivot's OUTGOING edges (Forward=&true semantics)
// filtered to the requested edge types.
func (s *scriptedGc) nodeSetEdges(ids []string, edgeTypes []string) []*knowledgev1.Edge {
	want := map[string]bool{}
	for _, et := range edgeTypes {
		want[et] = true
	}
	pivots := map[string]bool{}
	for _, id := range ids {
		pivots[id] = true
	}
	var out []*knowledgev1.Edge
	for id := range pivots {
		idEdges := s.edges[id]
		for i := range idEdges {
			e := &idEdges[i]
			if e.FromId != id {
				continue // outgoing-only
			}
			if len(want) > 0 && !want[e.Type] {
				continue
			}
			out = append(out, &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
		}
	}
	return out
}

// traversalResultsToProtoForRenderTest mirrors the tools-package helper so the
// render-package fakes can populate the typed traversal carrier.
func traversalResultsToProtoForRenderTest(results []engine.TraversalResult) []*knowledgev1.TraversalResult {
	out := make([]*knowledgev1.TraversalResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.TraversalResult{Node: r.Node, Distance: int32(r.Distance)}
	}
	return out
}

// renderViaIndex drives the FULL index pipeline against the fake — the same
// sequence InterceptQueryPlanTree's text path runs — so the existing render-tree
// assertions exercise RenderTreeFromIndex rather than the per-node RenderTree.
// It fetches the root, runs the subtree traversal, builds the child index,
// batches the depends-on edges, and renders. maxDepth mirrors RenderTree's arg.
func renderViaIndex(gc GraphCaller, root *knowledgev1.Node, maxDepth int) string {
	ctx := context.Background()
	nodes, structureEdges, edges := traverseForRenderTest(ctx, gc, root.Id, maxDepth)
	_ = edges
	childIndex, _ := BuildChildIndex(root.Id, nodes, structureEdges)
	allIDs := make([]string, 0, len(nodes)+1)
	allIDs = append(allIDs, root.Id)
	for _, n := range nodes {
		allIDs = append(allIDs, n.Id)
	}
	dependsOn, _ := FetchDependsOnEdges(ctx, gc, allIDs)
	var sb strings.Builder
	RenderTreeFromIndex(&sb, root, 0, maxDepth, childIndex, dependsOn)
	return sb.String()
}

// traverseForRenderTest issues the IncludeEdgeMetadata traversal against the fake
// and decodes nodes + structure edges (the render-package analog of
// TraverseDescendantsWithEdges, which lives in the tools package).
func traverseForRenderTest(ctx context.Context, gc GraphCaller, rootID string, depth int) ([]*knowledgev1.Node, []*knowledgev1.Edge, []knowledgev1.Edge) {
	fwd := true
	plan := &knowledgev1.QueryPlan{
		Selection:           &knowledgev1.Selection{FromId: []string{rootID}, EdgeTypes: []string{string(kgtypes.EdgeKGContains)}},
		Forward:             &fwd,
		ReturnMode:          knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
		IncludeEdgeMetadata: true,
	}
	if depth > 0 {
		plan.MaxHops = int32(depth)
	}
	resp, _ := gc.Execute(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: plan}})
	results, _ := engine.DecodeTraversal(resp)
	nodes := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		if r.Node.Id == "" || r.Node.Id == rootID {
			continue
		}
		nodes = append(nodes, r.Node)
	}
	edgeVals := engine.EdgesFromProto(resp.GetTraversalEdges())
	edges := make([]*knowledgev1.Edge, len(edgeVals))
	for i := range edgeVals {
		edges[i] = &edgeVals[i]
	}
	return nodes, edges, edgeVals
}

// TestRenderTree_ParentChildGrandchild seeds a 3-node fixture and
// asserts the rendered indentation + ID lines match the documented
// contract. The server-
// side renderer's output shape is the golden — this test pins the
// client-side port produces the same bytes.
func TestRenderTree_ParentChildGrandchild(t *testing.T) {
	gc := newScriptedGc().
		putNode("p", "parent", "plan", "active", "parent desc").
		putNode("c", "child", "phase", "pending", "child desc").
		putNode("g", "grandchild", "step", "pending", "grandchild desc").
		putEdges("p",
			map[string]string{"peer_id": "c", "relationship": "contains", "direction": "outgoing"}).
		putEdges("c",
			map[string]string{"peer_id": "g", "relationship": "contains", "direction": "outgoing"}).
		putEdges("g")

	parent := &knowledgev1.Node{
		Id:          "p",
		Type:        string(kgtypes.NodePlan),
		SymbolName:  "parent",
		Status:      "active",
		Description: "parent desc",
	}

	got := renderViaIndex(gc, parent, 5)

	expected := strings.Join([]string{
		"parent (plan) [active]",
		"  ID: p",
		"  child (phase) [pending]",
		"    child desc",
		"    ID: c",
		"    grandchild (step) [pending]",
		"      grandchild desc",
		"      ID: g",
		"",
	}, "\n")
	assert.Equal(t, expected, got)
}

// TestTopoSort_FiveNodeDependencyChain pins the topo-sort
// contract: a chain c5→c4→c3→c2→c1 sorts to
// [c1,c2,c3,c4,c5]. The dependency edge points from a child to its
// prerequisite — c5 "depends on c4" means c4 must come first.
func TestTopoSort_FiveNodeDependencyChain(t *testing.T) {
	// Input order is reversed so the natural sort is non-trivial.
	in := []walkChild{
		{node: &knowledgev1.Node{Id: "c5"}, dependsOn: "c4"},
		{node: &knowledgev1.Node{Id: "c4"}, dependsOn: "c3"},
		{node: &knowledgev1.Node{Id: "c3"}, dependsOn: "c2"},
		{node: &knowledgev1.Node{Id: "c2"}, dependsOn: "c1"},
		{node: &knowledgev1.Node{Id: "c1"}, dependsOn: ""},
	}
	got := topoSort(in)
	require.Len(t, got, 5)

	want := []string{"c1", "c2", "c3", "c4", "c5"}
	for i, w := range want {
		assert.Equal(t, w, got[i].node.Id, "position %d should be %s", i, w)
	}
}

// TestRenderTree_DependsOnReordersChildren verifies that two siblings
// with a depends-on edge between them render in topological order
// regardless of the source order returned by the edge iterator.
func TestRenderTree_DependsOnReordersChildren(t *testing.T) {
	// Edges return [b, a] but b depends-on a — render should be a then b.
	gc := newScriptedGc().
		putNode("p", "parent", "phase", "active", "").
		putNode("a", "step-a", "step", "pending", "").
		putNode("b", "step-b", "step", "pending", "").
		putEdges("p",
			map[string]string{"peer_id": "b", "relationship": "contains", "direction": "outgoing"},
			map[string]string{"peer_id": "a", "relationship": "contains", "direction": "outgoing"}).
		putEdges("a").
		putEdges("b",
			map[string]string{"peer_id": "a", "relationship": "depends-on", "direction": "outgoing"})

	parent := &knowledgev1.Node{
		Id:         "p",
		Type:       string(kgtypes.NodePhase),
		SymbolName: "parent",
		Status:     "active",
	}

	out := renderViaIndex(gc, parent, 5)

	// step-a must appear before step-b in the rendered output.
	aPos := strings.Index(out, "step-a")
	bPos := strings.Index(out, "step-b")
	require.NotEqual(t, -1, aPos, "step-a should be rendered")
	require.NotEqual(t, -1, bPos, "step-b should be rendered")
	assert.Less(t, aPos, bPos, "step-a (dependency target) must render before step-b (dependent)")
}

// TestRenderTree_ProxyAnnotationFallback verifies that a proxy node
// surfaces via proxyAnnotation in the rendered line. The server-side
// resolver enrichment is not available client-side; the
// metadata-only proxyAnnotation is the contracted client-side output.
func TestRenderTree_ProxyAnnotationFallback(t *testing.T) {
	proxy := &knowledgev1.Node{
		Id:         "px",
		Type:       string(kgtypes.NodeProxy),
		SymbolName: "proxy-name",
	}
	kgtypes.SetValue(proxy, "foreign_id", "remote-1")
	kgtypes.SetValue(proxy, "foreign_graph", "practice")

	gc := newScriptedGc().putEdges("px")
	out := renderViaIndex(gc, proxy, 1)

	// The output should at minimum include the proxy ID line + the
	// proxy annotation marker. The exact annotation shape comes from
	// helpers.proxyAnnotation; check for the bracketed marker.
	assert.Contains(t, out, "ID: px")
	assert.Contains(t, out, "[proxy")
}

// TestRenderTree_MissingChildSkipped asserts that an edge pointing to
// a node whose FetchNode returns ID="" (not found) is silently
// skipped — matches the slog.Warn-and-continue contract from the
// server-side walker.
func TestRenderTree_MissingChildSkipped(t *testing.T) {
	gc := newScriptedGc().
		putNode("p", "parent", "phase", "active", "").
		putNode("real", "real-child", "step", "pending", "").
		// "missing" intentionally has no putNode → FetchNode returns zero.
		putEdges("p",
			map[string]string{"peer_id": "missing", "relationship": "contains", "direction": "outgoing"},
			map[string]string{"peer_id": "real", "relationship": "contains", "direction": "outgoing"}).
		putEdges("real")

	parent := &knowledgev1.Node{Id: "p", Type: string(kgtypes.NodePhase), SymbolName: "parent", Status: "active"}
	out := renderViaIndex(gc, parent, 5)

	assert.Contains(t, out, "real-child", "real child must render")
	assert.NotContains(t, out, "missing", "missing-id text must not appear")
}

// TestRenderTree_DepthCutoff confirms that maxDepth prevents recursion
// below the cutoff. Compile a fixture with a 4-level chain, ask for
// depth=2, and check the leaf doesn't appear.
func TestRenderTree_DepthCutoff(t *testing.T) {
	gc := newScriptedGc()
	for i := range 4 {
		id := fmt.Sprintf("n%d", i)
		gc.putNode(id, "node-"+id, "step", "pending", "")
	}
	gc.putEdges("n0", map[string]string{"peer_id": "n1", "relationship": "contains", "direction": "outgoing"})
	gc.putEdges("n1", map[string]string{"peer_id": "n2", "relationship": "contains", "direction": "outgoing"})
	gc.putEdges("n2", map[string]string{"peer_id": "n3", "relationship": "contains", "direction": "outgoing"})
	gc.putEdges("n3")

	root := &knowledgev1.Node{Id: "n0", Type: string(kgtypes.NodeStep), SymbolName: "node-n0"}
	out := renderViaIndex(gc, root, 1)

	assert.Contains(t, out, "ID: n0")
	assert.Contains(t, out, "ID: n1")
	assert.NotContains(t, out, "ID: n2", "depth=1 should cut off at n1")
	assert.NotContains(t, out, "ID: n3")
}

// TestRenderTreeFromIndex_UpdatedSuffixParity pins the split-brain risk
// of the two renderers that are documented as byte-identical per node
// (tree.go:77-95): RenderTree walks per-node RPCs, RenderTreeFromIndex
// walks a prebuilt index, and every per-node line format change must
// land in both. This is the only test that goes red when the read-time
// provenance suffix is added to one renderer and missed in the other —
// the assemble-arm test reaches RenderTree alone, and plan_tree's
// goldens seed no timestamps.
func TestRenderTreeFromIndex_UpdatedSuffixParity(t *testing.T) {
	const ts = int64(1785548993004179000)

	gc := newScriptedGc().
		putNode("p", "parent", "plan", "active", "parent desc").
		putNode("c", "child", "phase", "pending", "child desc").
		putEdges("p",
			map[string]string{"peer_id": "c", "relationship": "contains", "direction": "outgoing"}).
		putEdges("c")
	gc.nodes["p"].UpdatedAt = ts
	gc.nodes["c"].UpdatedAt = ts

	// Both renderers walk the SAME root pointer, so any divergence is
	// the renderer's, never the fixture's.
	root := gc.nodes["p"]

	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, root, 0, 5)
	viaWalk := sb.String()
	viaIndex := renderViaIndex(gc, root, 5)

	assert.Equal(t, viaWalk, viaIndex,
		"RenderTree and RenderTreeFromIndex must stay byte-identical per node")
	assert.Contains(t, viaWalk, "(updated ", "RenderTree must carry the suffix")
	assert.Contains(t, viaIndex, "(updated ", "RenderTreeFromIndex must carry the suffix")
}
