// SPDX-License-Identifier: Apache-2.0

// thought.go — client-side intercept for the full `thoughts` MCP tool
// surface (think, charge, recall, trace, propagate; adjacency +
// charges_for stay server-side bulk-reads) and the `query` MCP tool's
// reflective + thought modes (personality, tensions, blind_spots,
// summary, clusters, evolution, influence, examine for NodeThought IDs,
// simulate, plus query() shapes carrying mode:timeline / mode:charges
// or any thought-property filter — valence_min/valence_max/
// magnitude_min/consistency_max/session/connected_to/status).
//
// Every thought-domain op is claimed client-side. The
// server-side handlers return the client-intercept-required sentinel
// (see cmd/knowledge-server/tools/tools_thought.go and tools_thought_query.go
// and tools_query_routes.go); InterceptThoughts is the only path that
// produces real results. Mirrors the shape of InterceptWorker — same
// name-filtering, same fall-through convention for non-thought paths
// like examine of generic nodes.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// thoughtsArgs is the parsed wire shape for thoughts(...) — only the
// fields InterceptThoughts needs to dispatch are decoded; everything
// else is left for the server-side handler when the call falls through.
type thoughtsArgs struct {
	Operation string `json:"operation"`
	Mode      string `json:"mode"`
	AllTypes  bool   `json:"all_types"`
	Format    string `json:"format"`
}

// queryReflectArgs is the parsed wire shape for query(mode:...) when the
// mode is one of the reflective surface. Only the dispatch-relevant
// fields are decoded.
//
// queryReflectArgs uses *flexFloat for the thought-property filters because
// the client-side intercept must accept both raw numbers and quoted-string
// numeric forms (some LLMs double-encode). The broader server-side
// queryArgs (cmd/knowledge-server/tools/tools_query_args.go:26-29) uses
// *float64 — it parses pre-routing wire shapes that don't currently need
// flex-typed decode.
type queryReflectArgs struct {
	Mode     string `json:"mode"`
	Cluster  string `json:"cluster"`
	ClusterA string `json:"cluster_a"`
	ClusterB string `json:"cluster_b"`
	Limit    int    `json:"limit"`
	Format   string `json:"format"`
	Graph    string `json:"graph"`
	// Thought-domain routing fields. Without these the intercept cannot
	// distinguish query(mode:examine, id:<thought>) from
	// query(mode:examine, id:<file_symbol>), nor can it pick up the
	// timeline/charges/thought-property filter shapes that route to recall.
	ID           string     `json:"id"`
	Text         string     `json:"text"`
	Type         string     `json:"type"`
	Session      string     `json:"session"`
	ConnectedTo  string     `json:"connected_to"`
	Status       string     `json:"status"`
	ValenceMin   *flexFloat `json:"valence_min"`
	ValenceMax   *flexFloat `json:"valence_max"`
	MagnitudeMin *flexFloat `json:"magnitude_min"`
	ConsistMax   *flexFloat `json:"consistency_max"`
}

// InterceptThoughts dispatches the reflective-surface operations of the
// `thoughts` and `query` MCP tools into the client-side
// cmd/knowledge/internal/thought package. Returns (true, result) when
// the call was handled; (false, zero) otherwise (the host falls back to
// forwarding to the server-side handler).
//
// Routing:
//   - thoughts(propagate): handled
//   - thoughts(recall, mode:"clusters"[, all_types]): handled
//   - query(mode in {personality, influence, tensions, blind_spots,
//     summary, evolution, clusters}): handled
//   - everything else: fall through to server.
func InterceptThoughts(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	switch params.Name {
	case "thoughts":
		return interceptThoughtsOp(deps, params)
	case "query":
		return interceptQueryReflect(deps, params)
	}
	return false, kgtools.ToolResult{}
}

// interceptThoughtsOp dispatches the reflective subset of the `thoughts`
// tool. Unrecognized ops fall through unchanged.
func interceptThoughtsOp(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a thoughtsArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Don't claim the call — let the server surface the parse
		// error so the message matches every other parse-error site.
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	switch a.Operation {
	case "think":
		return true, handleThinkClient(ctx, deps, params)
	case "charge":
		return true, handleChargeClient(ctx, deps, params)
	case "trace":
		return true, handleTraceClient(ctx, deps, params)
	case "recall":
		// handleRecallClient subsumes the cluster-mode special case
		// (mode:clusters early-returns to handleRecallClusters from
		// inside the handler — see intercept_thoughts_recall.go).
		return true, handleRecallClient(ctx, deps, params)
	case "propagate":
		return true, handlePropagateClient(ctx, deps)
	}
	return false, kgtools.ToolResult{}
}

// interceptQueryReflect dispatches the reflective subset of query(mode:...).
// Unrecognized modes fall through.
func interceptQueryReflect(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a queryReflectArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// Non-knowledge graphs are out of scope for the thought-domain intercept.
	// Mirror the server-side guard at cmd/knowledge-server/tools/tools_query.go:172-173.
	if a.Graph != "" && a.Graph != "knowledge" {
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	switch a.Mode {
	case "personality":
		return true, handleReflectPersonality(ctx, deps, a)
	case "influence":
		return true, handleReflectInfluence(ctx, deps, a)
	case "tensions":
		return true, handleReflectTensions(ctx, deps, a)
	case "blind_spots":
		return true, handleReflectBlindSpots(ctx, deps, a)
	case "summary":
		return true, handleReflectSummary(ctx, deps, a)
	case "evolution":
		return true, handleReflectEvolution(ctx, deps, a)
	case "clusters":
		return true, handleReflectClusters(ctx, deps, a)
	case "examine":
		// Gate on NodeThought — non-thought IDs fall through to the
		// server's generic inspector. Mirrors the server-side gate at
		// tools_query_inspect.go:63-70 (handleInspectNode thought-branch).
		if a.ID == "" {
			return false, kgtools.ToolResult{}
		}
		gc := deps.GraphCaller()
		if gc == nil {
			return false, kgtools.ToolResult{}
		}
		node, ok := clientthought.FetchNode(ctx, gc, a.ID)
		if !ok || kgtypes.NodeType(node.Type) != kgtypes.NodeThought {
			return false, kgtools.ToolResult{}
		}
		// Re-marshal as the {thought, format} shape handleExamineClient
		// expects. params.Arguments may carry extra fields the
		// handler doesn't read — pass through to keep the existing
		// helper API unchanged.
		examParams := kgtools.CallToolParams{
			Name:      params.Name,
			Arguments: marshalOrEmpty(map[string]any{"thought": a.ID, "format": a.Format}),
		}
		return true, handleExamineClient(ctx, deps, examParams)
	case "simulate":
		return true, handleSimulateClient(ctx, deps, params)
	}

	// Recall routing: query(mode:timeline / mode:charges) and any
	// query(...) carrying a thought-property filter route to recall, so the
	// client claims them here.
	recallModes := map[string]bool{"timeline": true, "charges": true}
	hasThoughtFilter := a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != "" || a.Status != ""
	if hasThoughtFilter || recallModes[a.Mode] {
		return true, handleRecallClient(ctx, deps, recallParamsFromQuery(params, a))
	}

	return false, kgtools.ToolResult{}
}

// recallParamsFromQuery translates queryReflectArgs into the recall
// payload shape handleRecallClient expects. Mirrors the server-side
// translation at cmd/knowledge-server/tools/tools_query_routes.go:143-167
// (the routeRecall builder).
func recallParamsFromQuery(params kgtools.CallToolParams, a queryReflectArgs) kgtools.CallToolParams {
	m := map[string]any{
		"query":     a.Text,
		"limit":     a.Limit,
		"mode":      a.Mode,
		"status":    a.Status,
		"all_types": a.Type == "all",
	}
	if a.Session != "" {
		m["session"] = a.Session
	}
	if a.ConnectedTo != "" {
		m["connected_to"] = a.ConnectedTo
	}
	if a.ValenceMin != nil {
		m["valence_min"] = float64(*a.ValenceMin)
	}
	if a.ValenceMax != nil {
		m["valence_max"] = float64(*a.ValenceMax)
	}
	if a.MagnitudeMin != nil {
		m["magnitude_min"] = float64(*a.MagnitudeMin)
	}
	if a.ConsistMax != nil {
		m["consistency_max"] = float64(*a.ConsistMax)
	}
	if a.Format != "" {
		m["format"] = a.Format
	}
	return kgtools.CallToolParams{
		Name:      params.Name,
		Arguments: marshalOrEmpty(m),
	}
}

// marshalOrEmpty marshals data as JSON; returns []byte("{}") on error so
// the downstream JSON unmarshal at the handler entry surfaces a structured
// validation error instead of crashing.
func marshalOrEmpty(data any) json.RawMessage {
	b, err := json.Marshal(data)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// handlePropagateClient runs DeGroot propagation client-side. Returns a
// rendered summary line matching the former server-side output shape.
func handlePropagateClient(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("propagate: graph client unavailable")
	}
	result, err := clientthought.RunPropagation(ctx, gc, nil, nil)
	if err != nil {
		return errorResult("propagate failed: " + err.Error())
	}
	return textResult(formatPropagationResult(result))
}

// fetchClusterContext runs cluster detection + personality computation
// in one synchronous pass. Used by every reflective handler that needs
// clusters or profile state. Returns empty values + a logged warning on
// failure so the format helpers can still render an "empty" report.
func fetchClusterContext(ctx context.Context, deps ClientDeps) ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, nil
	}
	clusters, err := clientthought.DetectThoughtClusters(ctx, gc, 0.5)
	if err != nil || len(clusters) == 0 {
		return clusters, nil
	}
	profile, err := clientthought.ComputePersonalityScalars(ctx, gc, clusters, nil)
	if err != nil {
		return clusters, nil
	}
	return clusters, &profile
}
