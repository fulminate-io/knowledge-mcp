// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryDecisions ports the server-side
// handleDecisions handler client-side. Claims query(type:"decision")
// for both the listing path (no text) and topic-search path (text
// set).
//
// Wire routing:
//   - Listing path: gc.Call("query", {type:"decision", limit, offset,
//     format:"json"}) → handleBrowseJSON returns {total, nodes:[...
//     knowledgev1.Node]}.
//   - Topic-search path: gc.Call("search", {query:<text>+" decision",
//     graph:"knowledge", types:["decision"], format:"json", limit})
//     → handleSearch's knowledge route emits the JSON envelope via
//     formatSearchResultsJSON. NOT the "query" tool — the query tool's
//     text path returns markdown unconditionally.
//
// Reuses engine.HydrateFromJSON (relocated from tools per finding
// 3bdc9695) for the topic-search JSON decode.
//
// FUL-251b Phase 3: must be wired BEFORE Phase 5 gates the
// server-side decision arm on format != "json".

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// InterceptQueryDecisions claims query(type:"decision"). Returns
// (true, result) on match; otherwise (false, _) so the chain
// continues.
func InterceptQueryDecisions(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Type != "decision" {
		return false, kgtools.ToolResult{}
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("query(decision): graph caller unavailable")
	}

	ctx := context.Background()
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 10
	}

	if a.Text != "" {
		results, err := fetchDecisionsTopic(ctx, gc, a.Text, limit)
		if err != nil {
			return true, errorResult("search: " + err.Error())
		}
		return true, formatDecisionsResults(results, 0, len(results))
	}

	results, total, err := fetchDecisionsListing(ctx, gc, limit, int(a.Offset))
	if err != nil {
		return true, errorResult(err.Error())
	}
	return true, formatDecisionsResults(results, int(a.Offset), total)
}

// fetchDecisionsTopic issues the topic-search wire call against
// handleSearch's JSON-emitting knowledge route and decodes via
// hydrateFromJSON. Defensive client-side type narrowing keeps only
// decision results (handleSearch's filterSearchResults already
// filters server-side, but the defensive narrow shields the renderer
// from any future change to the type-filter contract).
func fetchDecisionsTopic(ctx context.Context, gc GraphCaller, topic string, limit int) ([]engine.SearchResult, error) {
	args, err := json.Marshal(struct {
		Query string   `json:"query"`
		Graph string   `json:"graph"`
		Types []string `json:"types"`
		Limit int      `json:"limit"`
	}{
		Query: topic + " decision", Graph: "knowledge",
		Types: []string{"decision"}, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal search args: %w", err)
	}
	resp, err := executeSearch(ctx, gc, args)
	if err != nil {
		return nil, err
	}
	all, err := engine.DecodeSearch(resp)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if kgtypes.NodeType(r.Node.Type) == kgtypes.NodeDecision {
			out = append(out, r)
		}
	}
	return out, nil
}

// fetchDecisionsListing issues the listing-path wire call against
// handleBrowseJSON and wraps the response nodes as HydratedResults
// for the shared formatter.
func fetchDecisionsListing(ctx context.Context, gc GraphCaller, limit, offset int) ([]engine.SearchResult, int, error) {
	args, err := json.Marshal(struct {
		Type   string `json:"type"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}{Type: "decision", Limit: limit, Offset: offset})
	if err != nil {
		return nil, 0, fmt.Errorf("marshal listing args: %w", err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, 0, fmt.Errorf("list decisions: %w", err)
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, 0, fmt.Errorf("decode list decisions: %w", derr)
	}
	out := make([]engine.SearchResult, len(nodes))
	for i, n := range nodes {
		out[i] = engine.SearchResult{Node: n}
	}
	// The browse total rides the ExecuteResponse.total carrier (same value the
	// legacy {total} envelope carried); the pagination footer uses it.
	return out, int(resp.GetTotal()), nil
}

// formatDecisionsResults is the byte-for-byte port of formatDecisions
// at cmd/knowledge-server/tools/tools_knowledge_query.go:248-274.
// Pure function over []HydratedResult — no store reads.
func formatDecisionsResults(results []engine.SearchResult, offset, total int) kgtools.ToolResult {
	if len(results) == 0 {
		return kgtools.TextResult("No decisions found.")
	}
	var sb strings.Builder
	if total > len(results) {
		fmt.Fprintf(&sb, "Decisions (showing %d-%d of %d total, most recent first):\n\n",
			offset+1, offset+len(results), total)
	} else {
		fmt.Fprintf(&sb, "Decisions (%d):\n\n", len(results))
	}
	for _, r := range results {
		n := r.Node
		fmt.Fprintf(&sb, "■ %s\n", n.SymbolName)
		fmt.Fprintf(&sb, "  Choice: %s\n", kgtypes.Value(n, "choice"))
		fmt.Fprintf(&sb, "  Rationale: %s\n", kgtypes.Value(n, "rationale"))
		if alts := kgtypes.Value(n, "alternatives"); alts != "" {
			fmt.Fprintf(&sb, "  Alternatives: %s\n", alts)
		}
		fmt.Fprintf(&sb, "  ID: %s\n\n", n.Id)
	}
	if offset+len(results) < total {
		fmt.Fprintf(&sb, "_Use offset=%d to see more decisions._\n", offset+len(results))
	}
	return kgtools.TextResult(sb.String())
}
