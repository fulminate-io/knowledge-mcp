// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryRules ports the server-side
// handleRules handler client-side. Claims query(type:"rule").
//
// Wire shape: gc.Call("query", {type:"rule", limit:0, format:"json"})
// → handleBrowseJSON's {total, nodes:[...]} envelope. The scope
// filter is applied client-side via the same substring rule
// handleRules used: `strings.Contains(strings.ToLower(scope +
// description), strings.ToLower(filter))`.
//
// Must be wired BEFORE Phase 5 gates the
// server-side rule arm on format != "json". Internal wire calls
// still reach handleBrowseJSON because the gate fall-through at
// tools_query_routes.go preserves the JSON path.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// InterceptQueryRules claims query(type:"rule").
func InterceptQueryRules(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Type != "rule" {
		return false, kgtools.ToolResult{}
	}

	// queryArgs (the shared client mirror at tools_logs_args.go) has
	// no Scope field — and we intentionally do NOT widen it. Mirror
	// the server-side handleRules pattern at
	// cmd/knowledge-server/tools/tools_knowledge_query.go:395-398:
	// decode a LOCAL anonymous struct for the scope filter.
	var scopeArgs struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(params.Arguments, &scopeArgs)

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("rules: graph caller unavailable")
	}

	ctx := context.Background()
	allRules, err := fetchAllRules(ctx, gc)
	if err != nil {
		return true, errorResult(err.Error())
	}

	// Local scope substring filter — port of
	// tools_knowledge_query.go:404-410.
	var rules []*knowledgev1.Node
	for _, n := range allRules {
		if scopeArgs.Scope != "" && !strings.Contains(strings.ToLower(kgtypes.Value(n, "scope")+n.Description), strings.ToLower(scopeArgs.Scope)) {
			continue
		}
		rules = append(rules, n)
	}

	if len(rules) == 0 {
		if scopeArgs.Scope != "" {
			return true, kgtools.TextResult("No rules found matching scope: " + scopeArgs.Scope)
		}
		return true, kgtools.TextResult("No rules recorded. Use add_rule to add codebase constraints.")
	}

	return true, kgtools.TextResult(renderRulesMarkdown(rules))
}

// fetchAllRules issues the rule type-browse via the Execute carrier seam and
// decodes the nodes_json carrier (engine.DecodeNodes) — the full knowledgev1.Node
// payloads, including the scope/enforcement metadata the rules render reads.
func fetchAllRules(ctx context.Context, gc GraphCaller) ([]*knowledgev1.Node, error) {
	args, err := json.Marshal(struct {
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}{Type: "rule", Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("marshal rule fetch: %w", err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	return engine.DecodeNodes(resp)
}

// renderRulesMarkdown is the byte-for-byte port of
// handleRules's populated branch at tools_knowledge_query.go:419-431.
// Scope and Enforcement lines emit ONLY when the corresponding
// node.Value returns non-empty — load-bearing for golden parity.
func renderRulesMarkdown(rules []*knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rules (%d):\n\n", len(rules))
	for _, r := range rules {
		fmt.Fprintf(&sb, "- **%s**\n", r.SymbolName)
		fmt.Fprintf(&sb, "  %s\n", r.Description)
		if scope := kgtypes.Value(r, "scope"); scope != "" {
			fmt.Fprintf(&sb, "  Scope: %s\n", scope)
		}
		if enf := kgtypes.Value(r, "enforcement"); enf != "" {
			fmt.Fprintf(&sb, "  Enforcement: %s\n", enf)
		}
		fmt.Fprintf(&sb, "  ID: %s\n\n", r.Id)
	}
	return sb.String()
}
