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

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// scriptedGc is the carrier-backed tree-render fake (T-GTB3 Phase 6). It stores
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
	id := q.GetById()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(s.edges[id])}, nil
	}
	if n, ok := s.nodes[id]; ok {
		return enginetest.ResponseWithNode(n), nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestRenderTree_ParentChildGrandchild seeds a 3-node fixture and
// asserts the rendered indentation + ID lines match the documented
// contract (criterion 923dc9802c74be8172b6bc739f711671). The server-
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

	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, parent, 0, 5)

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
	assert.Equal(t, expected, sb.String())
}

// TestTopoSort_FiveNodeDependencyChain pins criterion
// b3ba6f05e2ada9723510bea822cc7c5d: a chain c5→c4→c3→c2→c1 sorts to
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

	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, parent, 0, 5)
	out := sb.String()

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
	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, proxy, 0, 1)

	out := sb.String()
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
	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, parent, 0, 5)

	out := sb.String()
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
	var sb strings.Builder
	RenderTree(context.Background(), gc, &sb, root, 0, 1)

	out := sb.String()
	assert.Contains(t, out, "ID: n0")
	assert.Contains(t, out, "ID: n1")
	assert.NotContains(t, out, "ID: n2", "depth=1 should cut off at n1")
	assert.NotContains(t, out, "ID: n3")
}
