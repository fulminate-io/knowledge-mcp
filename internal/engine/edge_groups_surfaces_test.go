// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The shared fixture. ONE graph, rendered through every censused surface, so the
// surfaces are provably showing the same thing rather than each passing against
// a fixture shaped to suit it.
const (
	surfCallerA = "a/x.go:CallerA"
	surfCallerB = "b/y.go:CallerB"
	surfKey1    = "a/x.go:1042:CALLS:Run"  // the ambiguous group
	surfKey2    = "a/x.go:2200:CALLS:Run"  // THE NEGATIVE CONTROL: same decl, different offset
	surfKey3    = "b/y.go:77:CALLS:Handle" // the dynamic group
	surfBound1  = "z/z1.go:Bound1"         // bound control #1
)

// surfaceNodes gives every endpoint a DISTINCT file, line and signature, so a
// three-facts assertion cannot pass by reading one node for all candidates.
func surfaceNodes() []*knowledgev1.Node {
	n := func(id, file string, line int32, sig string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, SymbolName: id, Type: "function", FilePath: file, StartLine: line, Signature: sig}
	}
	return []*knowledgev1.Node{
		n(surfCallerA, "a/x.go", 5, "func CallerA()"),
		n(surfCallerB, "b/y.go", 7, "func CallerB()"),
		n("p/a.go:Run", "p/a.go", 11, "func Run(ctx context.Context) error"),
		n("p/b.go:Run", "p/b.go", 22, "func Run(n int) string"),
		n("p/c.go:Run", "p/c.go", 33, "func Run()"),
		n("p/d.go:Run", "p/d.go", 44, "func Run(a, b int) bool"),
		n("h/a.go:Handle", "h/a.go", 55, "func Handle(w io.Writer)"),
		n("h/b.go:Handle", "h/b.go", 66, "func Handle(s string) error"),
		n(surfBound1, "z/z1.go", 77, "func Bound1()"),
	}
}

// surfaceEdges: three groups (ambiguous, negative-control, dynamic) plus TWO
// bound controls carrying no Method, no Evidence and no Confidence — the shape
// every edge in every graph has today.
func surfaceEdges() []knowledgev1.Edge {
	g := func(from, to, method, key string, conf float64) knowledgev1.Edge {
		return knowledgev1.Edge{FromId: from, ToId: to, Type: "CALLS", Method: method, Evidence: key, Confidence: conf}
	}
	bound := func(from, to string) knowledgev1.Edge {
		return knowledgev1.Edge{FromId: from, ToId: to, Type: "CALLS"}
	}
	amb, dyn := kgtypes.EdgeMethodAmbiguousName, kgtypes.EdgeMethodDynamic
	return []knowledgev1.Edge{
		// AMBIGUOUS group — closed, three candidates.
		g(surfCallerA, "p/a.go:Run", amb, surfKey1, 1.0/3.0),
		g(surfCallerA, "p/b.go:Run", amb, surfKey1, 1.0/3.0),
		g(surfCallerA, "p/c.go:Run", amb, surfKey1, 1.0/3.0),
		// NEGATIVE CONTROL — a SECOND reference in the SAME decl at a different
		// offset whose candidate set OVERLAPS the first (p/b.go:Run is in both)
		// while each keeps a unique member, so a collapse into one group is
		// distinguishable from correct behavior.
		g(surfCallerA, "p/b.go:Run", amb, surfKey2, 0.5),
		g(surfCallerA, "p/d.go:Run", amb, surfKey2, 0.5),
		// DYNAMIC group — open, two candidates.
		g(surfCallerB, "h/a.go:Handle", dyn, surfKey3, 0.5),
		g(surfCallerB, "h/b.go:Handle", dyn, surfKey3, 0.5),
		// BOUND CONTROLS.
		bound(surfCallerA, surfBound1),
		bound(surfCallerA, surfCallerB),
	}
}

func surfaceResults() []TraversalResult {
	out := []TraversalResult{}
	for i, n := range surfaceNodes() {
		d := 1
		if i == 0 {
			d = 0
		}
		out = append(out, TraversalResult{Distance: d, Node: n})
	}
	return out
}

// TestCandidateGroupsAcrossSurfaces exercises ONE fixture graph through every
// surface the arm-by-arm census found. The assertion is PER NAMED SURFACE, never
// an aggregate across them, so a fix landing on some surfaces and missing another
// cannot pass.
//
// EVERY SUBTEST ALSO ASSERTS THE BOUND CONTROLS ARE UNTOUCHED — the zero-change
// promise is the one this change is most likely to break quietly, and it is
// cheapest to catch here where real bound edges sit beside real groups.
func TestCandidateGroupsAcrossSurfaces(t *testing.T) {
	nodes := surfaceNodes()
	edges := surfaceEdges()
	groups, ungrouped := GroupCandidateEdges(edges)
	require.Len(t, groups, 3, "ambiguous + negative-control + dynamic")
	require.Len(t, ungrouped, 2, "exactly the two bound controls survive as plain edges")

	nodeIdx := nodeIndexByID(nodes)

	t.Run("traverse_text", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(surfaceResults()),
			TraversalEdges:   edgesToProtoForTest(edges),
		}
		out, err := renderTraversalResponse(resp, traverseContext{Start: surfCallerA, GraphName: "code", Direction: "out", Format: "text"})
		require.NoError(t, err)
		text := out.Content[0].Text

		assert.Equal(t, 3, strings.Count(text, " candidates - "), "three group blocks")
		assert.Contains(t, text, "exactly one is the real target", "the closed spelling")
		assert.Contains(t, text, "or beyond", "the open spelling")
		// The three facts, per candidate, from DISTINCT nodes.
		assert.Contains(t, text, "`p/a.go:Run` - p/a.go:11 - func Run(ctx context.Context) error")
		assert.Contains(t, text, "`h/b.go:Handle` - h/b.go:66 - func Handle(s string) error")
		// BOUND CONTROLS UNTOUCHED: still in the flat edges section.
		assert.Contains(t, text, "`"+surfCallerA+"` → `"+surfBound1+"`")
	})

	t.Run("traverse_json", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(surfaceResults()),
			TraversalEdges:   edgesToProtoForTest(edges),
		}
		out, err := renderTraversalResponse(resp, traverseContext{Start: surfCallerA, GraphName: "code", Direction: "out", Format: "json"})
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))

		rows := payload["edge_groups"].([]any)
		require.Len(t, rows, 3)
		semantics := map[string]int{}
		for _, r := range rows {
			semantics[r.(map[string]any)["semantics"].(string)]++
		}
		assert.Equal(t, 2, semantics["exactly-one-of"], "the ambiguous group and the negative control")
		assert.Equal(t, 1, semantics["one-of-these-or-beyond"], "the dynamic group")

		flat := payload["edges"].([]any)
		assert.Len(t, flat, 2, "BOUND CONTROLS UNTOUCHED: exactly the two bound edges")
	})

	t.Run("graph_wide_json", func(t *testing.T) {
		s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
			enginetest.ResponseWithNodes(nodes...),
			{Edges: edgesToProtoForTest(edges)},
		}}
		out, err := Dispatch(context.Background(), s.fn(), "traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))

		assert.Len(t, payload["edge_groups"].([]any), 3)
		assert.Len(t, payload["edges"].([]any), 2, "BOUND CONTROLS UNTOUCHED")
	})

	t.Run("analyze_arm", func(t *testing.T) {
		// Candidates render inside their group block and are NOT also plain
		// callers; the section count counts what it lists.
		callerGroups := groups
		plain := []*knowledgev1.Node{nodeIdx[surfBound1]}
		body := RenderAnalyzeNode(AnalyzeView{
			RepoLabel: "knowledge", Subject: nodeIdx[surfCallerA],
			Callers: plain, CallerGroups: callerGroups, Candidates: nodeIdx,
		})
		assert.Contains(t, body, "## Callers (1)", "the count counts the plain entries only")
		assert.Equal(t, 3, strings.Count(body, " candidates - "))
		for _, cand := range []string{"p/a.go:Run", "p/c.go:Run", "h/a.go:Handle"} {
			assert.NotContains(t, body, "### "+cand+" (function)", "candidate is not also a plain caller")
		}
		assert.Contains(t, body, "### "+surfBound1+" (function)", "BOUND CONTROL UNTOUCHED: still a plain caller")
	})

	t.Run("explain_arm", func(t *testing.T) {
		out := RenderExplainEdges("code", ungrouped, nodeIdx, groups)
		assert.Equal(t, 3, strings.Count(out, " candidates - "))
		assert.Equal(t, 2, strings.Count(out, "### Edge #"),
			"BOUND CONTROLS UNTOUCHED: exactly the two bound edges get a per-edge block")
		assert.NotContains(t, out, "Evidence (raw)", "the per-edge raw-evidence line is suppressed for members")
	})

	t.Run("correlations_arm", func(t *testing.T) {
		rows := make([]CorrelationEdgeRow, 0, len(ungrouped))
		for i := range ungrouped {
			rows = append(rows, CorrelationEdgeRow{
				Edge: copyGroupEdge(&ungrouped[i]), FromName: ungrouped[i].FromId, ToName: ungrouped[i].ToId,
				FromType: "function", ToType: "function",
			})
		}
		out := RenderCorrelations("code", rows, len(edges), false, groups)
		assert.Equal(t, 3, strings.Count(out, " candidates - "))
		assert.Equal(t, 2, strings.Count(out, "| CALLS |"),
			"BOUND CONTROLS UNTOUCHED: exactly the two bound edges get a table row")
		assert.Contains(t, out, "9 edge(s), sorted by confidence desc.", "total still counts every edge")
	})

	t.Run("frontier_depth2", func(t *testing.T) {
		// The ticket's depth-2 clause under the uniform short-circuit: a node
		// reachable ONLY through a group candidate is not listed, and the frontier
		// statement says so.
		onward := "q/onward.go:Deep"
		d2Edges := append(surfaceEdges(), knowledgev1.Edge{FromId: "p/a.go:Run", ToId: onward, Type: "CALLS"})
		d2Results := append(surfaceResults(), TraversalResult{Distance: 2, Node: &knowledgev1.Node{Id: onward, SymbolName: "Deep", Type: "function"}})
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(d2Results),
			TraversalEdges:   edgesToProtoForTest(d2Edges),
		}
		out, err := renderTraversalResponse(resp, traverseContext{Start: surfCallerA, GraphName: "code", Direction: "out", Format: "text"})
		require.NoError(t, err)
		text := out.Content[0].Text

		assert.NotContains(t, text, onward, "a node reachable only THROUGH a group is not listed")
		assert.Contains(t, text, "traversal stops at this candidate group")
		assert.Contains(t, text, surfBound1, "BOUND CONTROL UNTOUCHED: still reachable and listed")
	})

	t.Run("negative_control_distinct_groups", func(t *testing.T) {
		// The ticket's named negative control: two DIFFERENT references from the
		// same decl with OVERLAPPING candidate sets stay two groups.
		var g1, g2 *CandidateGroup
		for i := range groups {
			switch groups[i].Key {
			case surfKey1:
				g1 = &groups[i]
			case surfKey2:
				g2 = &groups[i]
			}
		}
		require.NotNil(t, g1)
		require.NotNil(t, g2)
		assert.Len(t, g1.Members, 3)
		assert.Len(t, g2.Members, 2)

		count := func(g *CandidateGroup, id string) int {
			n := 0
			for i := range g.Members {
				if g.Members[i].ToId == id {
					n++
				}
			}
			return n
		}
		assert.Equal(t, 1, count(g1, "p/b.go:Run"), "the shared candidate appears ONCE in the first group")
		assert.Equal(t, 1, count(g2, "p/b.go:Run"), "and ONCE in the second, never twice in one")
		assert.Equal(t, 1, count(g1, "p/a.go:Run"), "each group keeps its unique member")
		assert.Equal(t, 1, count(g2, "p/d.go:Run"))
	})
}
