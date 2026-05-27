// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/google/uuid"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// assembleTestPlan renders a NodeTestPlan: header + RenderTree walk
// then either:
//
//   - newRun=true: atomically create N pending test_run nodes via
//     mutate(create_batch) and surface the new session ID.
//   - newRun=false: enumerate existing test_runs per step, optionally
//     filtered by runSession.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:206.
// The newRun=true path replaces the direct store.Store().CreateBatch
// call with one create_batch routed through the Execute carrier seam
// (engine.Compile("mutate")+Execute) so the create lands inside the
// server's atomic txn from the client side.
func assembleTestPlan(
	ctx context.Context,
	gc GraphCaller,
	node *knowledgev1.Node,
	newRun bool,
	runSession string,
) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Test Plan: %s\n\n", node.SymbolName)
	RenderTree(ctx, gc, &sb, node, 0, 3)

	// Collect step IDs.
	stepEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
	var steps []*knowledgev1.Node
	for _, e := range stepEdges {
		sn, err := FetchNode(ctx, gc, e.ToId)
		if err != nil || sn == nil {
			continue
		}
		if kgtypes.NodeType(sn.Type) == kgtypes.NodeTestStep {
			steps = append(steps, sn)
		}
	}

	if newRun {
		return assembleTestPlanNewRun(ctx, gc, &sb, steps)
	}

	// Show runs per step (filtered by run_session if provided).
	fmt.Fprintf(&sb, "\n## Test Runs\n\n")
	for _, step := range steps {
		childEdges, _ := IterEdges(ctx, gc, step.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
		var runs []*knowledgev1.Node
		for _, e := range childEdges {
			cn, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || cn == nil {
				continue
			}
			if kgtypes.NodeType(cn.Type) != kgtypes.NodeTestRun {
				continue
			}
			if runSession != "" && kgtypes.Value(cn, "run_session") != runSession {
				continue
			}
			runs = append(runs, cn)
		}
		if len(runs) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n", step.SymbolName)
		for _, r := range runs {
			session := kgtypes.Value(r, "run_session")
			fmt.Fprintf(&sb, "  - [%s] %s (session: %s) — ID: %s\n", r.Status, r.SymbolName, session, r.Id)
		}
	}
	return kgtools.TextResult(sb.String())
}

// assembleTestPlanNewRun fires exactly one create_batch Execute
// (operation:"create_batch", nodes:[...], edges:[...]) to create N
// pending test_run nodes + contained-by edges in a single atomic
// txn. Extracted to keep assembleTestPlan under the funlen cap.
//
// Wire shape (matches Phase 0's nodeCreateItem / edgeCreateItem):
//
//	{
//	  operation: "create_batch",
//	  nodes: [{type:"test_run", name:<step.SymbolName>, status:"pending",
//	           metadata:{"run_session":<sessionID>}}, …],
//	  edges: [{from_id:<step.Id>, to_idx:<i>, type:"contains"}, …]
//	}
//
// Returns the rendered text + appended session-summary footer.
func assembleTestPlanNewRun(
	ctx context.Context,
	gc GraphCaller,
	sb *strings.Builder,
	steps []*knowledgev1.Node,
) kgtools.ToolResult {
	sessionID := uuid.New().String()
	if len(steps) == 0 {
		fmt.Fprintf(sb, "\n## New Run Session\n\nSession ID: %s\nCreated 0 pending test_run nodes.\n", sessionID)
		return kgtools.TextResult(sb.String())
	}

	type batchNode struct {
		Type     string            `json:"type"`
		Name     string            `json:"name"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
	}
	type batchEdge struct {
		FromID string `json:"from_id"`
		ToIdx  int    `json:"to_idx"`
		Type   string `json:"type"`
	}

	nodes := make([]batchNode, len(steps))
	edges := make([]batchEdge, len(steps))
	for i, step := range steps {
		nodes[i] = batchNode{
			Type:     string(kgtypes.NodeTestRun),
			Name:     step.SymbolName,
			Status:   string(kgtypes.StatusPending),
			Metadata: map[string]string{"run_session": sessionID},
		}
		edges[i] = batchEdge{
			FromID: step.Id,
			ToIdx:  i,
			Type:   string(kgtypes.EdgeKGContains),
		}
	}
	payload := struct {
		Operation string      `json:"operation"`
		Nodes     []batchNode `json:"nodes"`
		Edges     []batchEdge `json:"edges"`
	}{
		Operation: "create_batch",
		Nodes:     nodes,
		Edges:     edges,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return kgtools.ErrorResult("create test runs: marshal: " + err.Error())
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return kgtools.ErrorResult("create test runs: " + err.Error())
	}
	req, ok := engine.Compile("mutate", raw)
	if !ok {
		return kgtools.ErrorResult("create test runs: create_batch not reducible to an ExecuteRequest")
	}
	if _, err := ex.Execute(ctx, req); err != nil {
		return kgtools.ErrorResult("create test runs: " + err.Error())
	}

	fmt.Fprintf(sb, "\n## New Run Session\n\nSession ID: %s\nCreated %d pending test_run nodes.\n", sessionID, len(nodes))
	return kgtools.TextResult(sb.String())
}
