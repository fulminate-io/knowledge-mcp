// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

func TestHandleTraceClient_EmptyThought_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleTraceClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"trace"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "'thought' (starting thought node ID) is required")
}

func TestFormatTraceStepsClient_EmptySteps(t *testing.T) {
	out := formatTraceStepsClient("start-id", nil)
	assert.Contains(t, out, "Trace from start-id: no neighbors")
}

func TestFormatTraceStepsClient_StepArrow(t *testing.T) {
	steps := []clientthought.TraceStep{
		{
			Node:      &knowledgev1.Node{Id: "t-2", Type: string(kgtypes.NodeThought), SymbolName: "second"},
			Depth:     1,
			Direction: "forward",
			EdgeType:  kgtypes.EdgeRelatesTo,
			Properties: clientthought.ThoughtProperties{
				Valence:   0.5,
				Magnitude: 1.2,
			},
		},
		{
			Node:      &knowledgev1.Node{Id: "t-3", Type: string(kgtypes.NodeThought), SymbolName: "third"},
			Depth:     1,
			Direction: "backward",
			EdgeType:  kgtypes.EdgeNext,
		},
	}
	out := formatTraceStepsClient("t-1", steps)
	assert.Contains(t, out, "Trace from t-1 — 2 step(s):")
	// forward → arrow.
	assert.Contains(t, out, "→ [thought] second (t-2)")
	// backward ← arrow.
	assert.Contains(t, out, "← [thought] third (t-3)")
}
