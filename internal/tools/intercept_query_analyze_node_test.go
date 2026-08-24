// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"slices"
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

// analyzeFake routes Execute over a fixture call graph: ByID → subject;
// traverse(CALLS,in) → callers; traverse(CALLS,out) → callees; and the same two
// directions over TEST_CALLS → testCallers/testCallees. Records the MaxHops of
// each production traversal so the depth clamp can be asserted, and the edge
// types of every traversal so the opt-in itself is assertable.
//
// IT HONORS Selection.EdgeTypes, exactly as the server does. A fake that served
// the same fixture to every edge type would hand the production call graph back
// to the TEST_CALLS walk and render every group twice.
type analyzeFake struct {
	subject    knowledgev1.Node
	callers    []knowledgev1.Node
	callees    []knowledgev1.Node
	callerHops int32
	calleeHops int32
	// callerEdges/calleeEdges ride the traversal responses as the edge-metadata
	// carrier; siblings answers the enrichment's pivot read and hydrate answers
	// its bulk node read. All four default to empty, so every pre-existing test
	// keeps the exact behavior it had before groups existed.
	callerEdges []knowledgev1.Edge
	calleeEdges []knowledgev1.Edge
	siblings    []knowledgev1.Edge
	hydrate     []*knowledgev1.Node
	// The TEST_CALLS side, empty by default — which is what every graph looks
	// like until it is re-collected against a collector that emits the edge.
	testCallers     []knowledgev1.Node
	testCallees     []knowledgev1.Node
	testCallerEdges []knowledgev1.Edge
	testCalleeEdges []knowledgev1.Edge
	// requestedEdgeTypes records one entry per traversal Execute, in order.
	requestedEdgeTypes [][]string
}

func (f *analyzeFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		types := q.GetSelection().GetEdgeTypes()
		f.requestedEdgeTypes = append(f.requestedEdgeTypes, slices.Clone(types))
		testSide := slices.Contains(types, string(kgtypes.EdgeTestCalls))

		var nodes []knowledgev1.Node
		var edges []knowledgev1.Edge
		switch {
		case testSide && q.GetForward():
			nodes, edges = f.testCallees, f.testCalleeEdges
		case testSide:
			nodes, edges = f.testCallers, f.testCallerEdges
		case q.GetForward():
			f.calleeHops = q.GetMaxHops()
			nodes, edges = f.callees, f.calleeEdges
		default:
			f.callerHops = q.GetMaxHops()
			nodes, edges = f.callers, f.callerEdges
		}
		results := make([]engine.TraversalResult, len(nodes))
		for i := range nodes {
			results[i] = engine.TraversalResult{Node: &nodes[i], Distance: 1}
		}
		return &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(results),
			TraversalEdges:   edgesToProtoForTest(edges),
		}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(edgesToProtoForTest(f.siblings), q)}, nil
	}
	if len(q.GetIds()) > 0 {
		return &knowledgev1.ExecuteResponse{Nodes: f.hydrate}, nil
	}
	// ByID subject.
	resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&f.subject}...)
	return resp, nil
}

// TestComposeAnalyzeNode covers the analyze recipe + render shape + depth clamp.
func TestComposeAnalyzeNode(t *testing.T) {
	f := &analyzeFake{
		subject: knowledgev1.Node{Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 10, EndLine: 20, Signature: "func Foo()"},
		callers: []knowledgev1.Node{
			{Id: "f.go:Bar", SymbolName: "Bar", Type: "function", FilePath: "f.go", StartLine: 30},
		},
		callees: []knowledgev1.Node{
			{Id: "g.go:Baz", SymbolName: "Baz", Type: "function", FilePath: "g.go", StartLine: 5},
		},
	}
	res := composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: "f.go:Foo", Repo: "knowledge"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)

	assert.Contains(t, body, "[knowledge]")
	assert.Contains(t, body, "# Foo (function) — f.go:10-20")
	assert.Contains(t, body, "**Signature:** `func Foo()`")
	assert.Contains(t, body, "## Callers (1)")
	assert.Contains(t, body, "### Bar (function) — f.go:30")
	assert.Contains(t, body, "## Callees (1)")
	assert.Contains(t, body, "### Baz (function) — g.go:5")

	// Default depths clamp to 1.
	assert.Equal(t, int32(1), f.callerHops)
	assert.Equal(t, int32(1), f.calleeHops)
}

// TestComposeAnalyzeNode_EmptyCases asserts the no-callers/no-callees lines.
func TestComposeAnalyzeNode_EmptyCases(t *testing.T) {
	f := &analyzeFake{subject: knowledgev1.Node{Id: "x", SymbolName: "X", Type: "function"}}
	res := composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: "x", Repo: "r"})
	body := textBodyTools(res)
	assert.Contains(t, body, "## Callers (0)")
	assert.Contains(t, body, "No callers found.")
	assert.Contains(t, body, "## Callees (0)")
	assert.Contains(t, body, "No callees found.")
}

// TestComposeAnalyzeNode_DepthClamp asserts caller_depth/callee_depth clamp 1..3.
func TestComposeAnalyzeNode_DepthClamp(t *testing.T) {
	f := &analyzeFake{subject: knowledgev1.Node{Id: "x", SymbolName: "X"}}
	composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: "x", Repo: "r", CallerDepth: 9, CalleeDepth: 0})
	assert.Equal(t, int32(3), f.callerHops, "caller_depth 9 clamps to 3")
	assert.Equal(t, int32(1), f.calleeHops, "callee_depth 0 clamps to 1")
}

// TestInterceptQueryAnalyzeNode_Gate asserts the graph=code+id+!stats gate.
func TestInterceptQueryAnalyzeNode_Gate(t *testing.T) {
	// graph=knowledge → not claimed.
	handled, _ := InterceptQueryAnalyzeNode(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"knowledge","id":"x"}`)})
	assert.False(t, handled)
	// graph=code mode=stats → not claimed (code stats is a separate composer).
	handled, _ = InterceptQueryAnalyzeNode(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","id":"x","mode":"stats"}`)})
	assert.False(t, handled)
	// graph=code no id → not claimed (that's code search).
	handled, _ = InterceptQueryAnalyzeNode(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","text":"x"}`)})
	assert.False(t, handled)
}

// TestComposeAnalyzeNode_CandidateGroups pins the analyze arm's group rendering:
// one block per group, candidates never ALSO listed as plain callers, and a
// section count that counts what it lists.
func TestComposeAnalyzeNode_CandidateGroups(t *testing.T) {
	const subjectID = "f.go:Foo"
	const ambSource = "f.go:Amb"
	const groupKey = "f.go:99:CALLS:Run"

	// One PLAIN caller and one THREE-candidate group, so a header that summed
	// both (N=4) is distinguishable from the correct N=1.
	newFake := func() *analyzeFake {
		return &analyzeFake{
			subject: knowledgev1.Node{Id: subjectID, SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 10, EndLine: 20},
			callers: []knowledgev1.Node{
				{Id: "f.go:Plain", SymbolName: "Plain", Type: "function", FilePath: "f.go", StartLine: 30},
				{Id: "p/a.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/a.go", StartLine: 10, Signature: "func Run() error"},
				{Id: "p/b.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/b.go", StartLine: 20, Signature: "func Run(n int)"},
				{Id: "p/c.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/c.go", StartLine: 30, Signature: "func Run(s string)"},
			},
			callerEdges: []knowledgev1.Edge{
				{FromId: "f.go:Plain", ToId: subjectID, Type: "CALLS"},
				{FromId: ambSource, ToId: "p/a.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
				{FromId: ambSource, ToId: "p/b.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
				{FromId: ambSource, ToId: "p/c.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
			},
		}
	}

	t.Run("group_renders_as_one_block", func(t *testing.T) {
		f := newFake()
		res := composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: subjectID, Repo: "knowledge"})
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 1, strings.Count(body, "one of 3 candidates"), "exactly ONE group block")
		assert.Contains(t, body, "exactly one is the real target")
	})

	t.Run("candidates_are_not_also_plain_callers", func(t *testing.T) {
		// THE DOUBLE-COUNT CATCHER.
		f := newFake()
		body := textBodyTools(composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: subjectID, Repo: "knowledge"}))
		for _, cand := range []string{"p/a.go", "p/b.go", "p/c.go"} {
			assert.NotContains(t, body, "### Run (function) — "+cand,
				"candidate %s is rendered inside its group block, never as a plain caller entry", cand)
		}
		assert.Contains(t, body, "### Plain (function) — f.go:30", "the genuine plain caller still renders")
	})

	t.Run("callers_count_matches_plain_callers", func(t *testing.T) {
		f := newFake()
		body := textBodyTools(composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: subjectID, Repo: "knowledge"}))
		assert.Contains(t, body, "## Callers (1)", "the count counts the entries listed, not those plus the group's candidates")
		assert.NotContains(t, body, "## Callers (4)")
	})
}
