// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// analyzeFake routes Execute over a fixture call graph: ByID → subject;
// traverse(CALLS,in) → callers; traverse(CALLS,out) → callees. Records the
// MaxHops of each traversal so the depth clamp can be asserted.
type analyzeFake struct {
	subject    knowledgev1.Node
	callers    []knowledgev1.Node
	callees    []knowledgev1.Node
	callerHops int32
	calleeHops int32
}

func (f *analyzeFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		var nodes []knowledgev1.Node
		if q.GetForward() {
			f.calleeHops = q.GetMaxHops()
			nodes = f.callees
		} else {
			f.callerHops = q.GetMaxHops()
			nodes = f.callers
		}
		results := make([]engine.TraversalResult, len(nodes))
		for i := range nodes {
			results[i] = engine.TraversalResult{Node: &nodes[i], Distance: 1}
		}
		return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}, nil
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
