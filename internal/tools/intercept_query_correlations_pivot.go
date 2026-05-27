// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_correlations_pivot.go is the client-side claim for the
// composite query modes correlations + pivot on NON-logs graphs (logs is owned
// by InterceptLogsQuery earlier in the chain). Ports the server
// handleGenericCorrelations (tools_query_correlations.go) and handleGenericPivot
// (tools_query_pivot.go) recipes over generic Execute primitives.
//
// BOUNDED-CONSTANT (correlations): the server walks every node then per-node
// IterEdges (N edge-iterations). The client issues exactly TWO Execute calls —
// ONE node-set fetch (Match empty, Limit 0, RETURN_MODE_NODES) and ONE
// RETURN_MODE_EDGES over the collected ids[] (Forward=true → outgoing edges,
// matching the server's per-node OutgoingEdges loop) — never a per-node edge
// fetch. Cite finding 1eef9499 item 1 (RETURN_MODE_EDGES ids[] node-set form).

// InterceptQueryCorrelationsPivot claims query(mode in {correlations,pivot}) for
// non-logs graphs.
func InterceptQueryCorrelationsPivot(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph == "logs" {
		return false, kgtools.ToolResult{} // logs owned by InterceptLogsQuery.
	}
	if a.Mode != "correlations" && a.Mode != "pivot" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphClient()
	if gc == nil {
		return true, errorResult(a.Mode + ": graph client unavailable")
	}
	ctx := context.Background()
	if a.Mode == "correlations" {
		return true, composeCorrelations(ctx, gc.Execute, a)
	}
	return true, composePivot(ctx, gc.Execute, a)
}

// composeCorrelations issues the bounded two-Execute recipe and renders.
func composeCorrelations(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	label := domainGraphLabel(a)
	target := domainTarget(a)

	// (1) ONE node-set fetch (Match empty, Limit 0).
	nodesResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodeSetPlan(a.IncludeTombstones)},
		Target: target,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("correlations fetch failed: %v", err))
	}
	nodes, derr := engine.DecodeNodes(nodesResp)
	if derr != nil {
		return errorResult("correlations decode failed: " + derr.Error())
	}
	nameByID := make(map[string]*knowledgev1.Node, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nameByID[n.Id] = n
		ids = append(ids, n.Id)
	}

	// (2) ONE RETURN_MODE_EDGES over the collected ids[] (outgoing, matching the
	// server per-node OutgoingEdges loop). Never a per-node edge fetch.
	rows, eerr := fetchCorrelationRows(ctx, exec, target, ids, nameByID, a)
	if eerr != nil {
		return errorResult(fmt.Sprintf("correlations fetch failed: %v", eerr))
	}
	if len(rows) == 0 {
		filterMsg := "any edge type"
		if len(a.EdgeType) > 0 {
			filterMsg = strings.Join(a.EdgeType, ", ")
		}
		return textResult(engine.RenderCorrelationsEmpty(label, filterMsg))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Edge.Confidence != rows[j].Edge.Confidence {
			return rows[i].Edge.Confidence > rows[j].Edge.Confidence
		}
		if rows[i].Edge.FromId != rows[j].Edge.FromId {
			return rows[i].Edge.FromId < rows[j].Edge.FromId
		}
		return rows[i].Edge.ToId < rows[j].Edge.ToId
	})
	return textResult(engine.RenderCorrelations(label, rows))
}

// fetchCorrelationRows issues the single bulk RETURN_MODE_EDGES Execute over the
// node-id set, filters by edge_type (client-side, mirroring the server typeSet),
// and shapes the rows. An empty id set yields no rows (no Execute).
func fetchCorrelationRows(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, ids []string, nameByID map[string]*knowledgev1.Node, a queryArgs) ([]engine.CorrelationEdgeRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	forward := true
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Ids:               ids,
			Forward:           &forward,
			ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			IncludeTombstones: a.IncludeTombstones,
		}},
		Target: target,
	})
	if err != nil {
		return nil, err
	}
	edges, derr := engine.DecodeEdges(resp)
	if derr != nil {
		return nil, derr
	}
	typeSet := correlationEdgeTypeSet(a.EdgeType)
	var rows []engine.CorrelationEdgeRow
	for i := range edges {
		e := &edges[i]
		if typeSet != nil && !typeSet[e.Type] {
			continue
		}
		fromName, fromType := correlationEndpoint(e.FromId, nameByID)
		toName, toType := correlationEndpoint(e.ToId, nameByID)
		// Construct the row as a fresh composite literal directly into append,
		// building the embedded Edge field-by-field. A `buildRow(...)` helper that
		// returned the row by value would copylocks the proto MessageState on the
		// return + append; an in-place fresh literal is lock-clean.
		rows = append(rows, engine.CorrelationEdgeRow{
			Edge: knowledgev1.Edge{
				FromId:        e.FromId,
				ToId:          e.ToId,
				Type:          e.Type,
				Weight:        e.Weight,
				Confidence:    e.Confidence,
				Method:        e.Method,
				Evidence:      e.Evidence,
				LastValidated: e.LastValidated,
			},
			FromName: fromName,
			FromType: fromType,
			ToName:   toName,
			ToType:   toType,
		})
	}
	return rows, nil
}

func correlationEdgeTypeSet(edgeTypes []string) map[string]bool {
	if len(edgeTypes) == 0 {
		return nil
	}
	out := make(map[string]bool, len(edgeTypes))
	for _, t := range edgeTypes {
		out[t] = true
	}
	return out
}

// correlationEndpoint resolves an edge endpoint id to its display name + node
// type via the hydrated name map, falling back to the raw id when absent.
func correlationEndpoint(id string, nameByID map[string]*knowledgev1.Node) (string, kgtypes.NodeType) {
	if n, ok := nameByID[id]; ok {
		return engine.CorrelationNodeName(n), kgtypes.NodeType(n.Type)
	}
	return id, ""
}

// composePivot validates rows/cols, fetches the candidate node set, builds the
// matrix, and renders. Port of handleGenericPivot.
func composePivot(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	if a.Rows == "" || a.Cols == "" {
		return errorResult("pivot requires rows and cols when graph is not logs")
	}
	if a.Rows == a.Cols {
		return errorResult(fmt.Sprintf("rows and cols must differ (both were %q)", a.Rows))
	}
	nodes, err := pivotFetchNodesClient(ctx, exec, a)
	if err != nil {
		return errorResult(fmt.Sprintf("pivot fetch failed: %v", err))
	}
	m := engine.BuildPivotMatrix(nodes, a.Rows, a.Cols)
	return textResult(engine.RenderPivotMatrix(domainGraphLabel(a), m))
}

// pivotFetchNodesClient pulls the candidate node set: by type (Match), by text
// (QSearch), or every node (Match empty) — all Limit 0 (no cap; a truncated
// pivot would mislead). Port of pivotFetchNodes.
func pivotFetchNodesClient(ctx context.Context, exec engine.ExecuteFn, a queryArgs) ([]*knowledgev1.Node, error) {
	target := domainTarget(a)
	switch {
	case a.Type != "":
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection:         &knowledgev1.Selection{NodeType: a.Type},
				IncludeTombstones: a.IncludeTombstones,
			}},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	case a.Text != "":
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Queries:           []string{a.Text},
				ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
				IncludeTombstones: a.IncludeTombstones,
			}},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		results, derr := engine.DecodeSearch(resp)
		if derr != nil {
			return nil, derr
		}
		out := make([]*knowledgev1.Node, 0, len(results))
		for _, r := range results {
			out = append(out, r.Node)
		}
		return out, nil
	default:
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodeSetPlan(a.IncludeTombstones)},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}
}

// nodeSetPlan builds the Match-empty Limit-0 RETURN_MODE_NODES plan (the full
// node-set fetch). An empty Selection NodeType means "match every node".
func nodeSetPlan(includeTombstones bool) *knowledgev1.QueryPlan {
	return &knowledgev1.QueryPlan{
		Selection:         &knowledgev1.Selection{},
		IncludeTombstones: includeTombstones,
	}
}

// domainTarget builds the GraphSelector for a composite-mode query from the
// generic args (graph + per-graph instance key).
func domainTarget(a queryArgs) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{
		Graph:    a.Graph,
		Repo:     a.Repo,
		Account:  a.Account,
		Name:     a.Name,
		Language: a.Language,
	}
}

// domainGraphLabel returns a human label for the target graph used in the
// composite-mode headers, mirroring the server's per-graph label.
func domainGraphLabel(a queryArgs) string {
	switch a.Graph {
	case "", "knowledge":
		return "knowledge"
	case "cloud", "cicd", "practice", "linkage", "code":
		if a.Account != "" {
			return a.Graph + ":" + a.Account
		}
		if a.Repo != "" {
			return a.Graph + ":" + a.Repo
		}
		if a.Language != "" {
			return a.Graph + ":" + a.Language
		}
		return a.Graph
	default:
		return a.Graph
	}
}
