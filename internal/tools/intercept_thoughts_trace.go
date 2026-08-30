// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_trace.go — client-side claim for
// thoughts(operation:trace). The server-side handleTrace body collapses
// is gone post-Phase-5; this is the only path that produces a
// real response.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// traceClientArgs is the parsed thoughts(operation:trace) shape. Mirrors
// the server-side struct at tools_thought_query.go:213-221.
type traceClientArgs struct {
	Thought          string `json:"thought"`
	Direction        string `json:"direction"`
	Depth            int    `json:"depth"`
	IncludeCharges   bool   `json:"include_charges"`
	IncludeArtifacts bool   `json:"include_artifacts"`
	Format           string `json:"format"`
}

// handleTraceClient claims thoughts(operation:trace). Validates the
// starting thought exists via FetchNode, walks the BFS via
// TraceThoughts, and renders through formatTraceSteps.
func handleTraceClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a traceClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	if a.Thought == "" {
		return errorResult("trace: 'thought' (starting thought node ID) is required")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("trace: graph client unavailable")
	}

	if _, ok := clientthought.FetchNode(ctx, gc, a.Thought); !ok {
		return errorResult(fmt.Sprintf("trace: thought %s not found", a.Thought))
	}

	steps, err := clientthought.TraceThoughts(ctx, gc, a.Thought, a.Direction, a.Depth, a.IncludeCharges, a.IncludeArtifacts)
	if err != nil {
		return errorResult("trace failed: " + err.Error())
	}

	if a.Format == "json" {
		return jsonResult(map[string]any{"start": a.Thought, "steps": steps, "total": len(steps)})
	}
	return textResult(formatTraceStepsClient(a.Thought, steps))
}

// formatTraceStepsClient renders the trace. Verbatim from
// the prior server-side tools_thought_query.go formatter.
func formatTraceStepsClient(startID string, steps []clientthought.TraceStep) string {
	if len(steps) == 0 {
		return fmt.Sprintf("Trace from %s: no neighbors at the requested depth.", startID)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Trace from %s — %d step(s):\n\n", startID, len(steps))
	for _, s := range steps {
		indent := strings.Repeat("  ", s.Depth)
		dirArrow := "→"
		if s.Direction == "backward" {
			dirArrow = "←"
		}
		if s.Node.Type == "thought" {
			fmt.Fprintf(&sb, "%s%s [%s] %s (%s) v:%.2f m:%.2f -[%s]\n",
				indent, dirArrow, s.Node.Type, s.Node.SymbolName, s.Node.Id,
				s.Properties.Valence, s.Properties.Magnitude, s.EdgeType)
		} else {
			fmt.Fprintf(&sb, "%s%s [%s] %s (%s) -[%s]\n",
				indent, dirArrow, s.Node.Type, s.Node.SymbolName, s.Node.Id, s.EdgeType)
		}
	}
	return sb.String()
}
