// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryListProjects ports the server-side
// handleListProjects + handleListProjectsJSON handlers client-side.
// Claims query(type in {plan, project, ticket, research, document})
// when no text-search arg is present. Text-search calls fall through
// to InterceptSearch / server-side hybrid search.
//
// Wire pattern: for each container type, issue
//   gc.Call("query", {type, format:"json", limit:0})
// which the server's handleBrowseJSON answers with {total, nodes:[...]}.
// Accumulate the nodes locally and render the same markdown/json shape
// the server-side handlers produced.
//
// FUL-251b Phase 2: must be wired BEFORE Phase 5 deletes the
// server-side dispatch arm.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// containerTypesForListProjects mirrors the server-side container
// type-set at cmd/knowledge-server/tools/tools_walk.go:223-228.
var containerTypesForListProjects = []kgtypes.NodeType{
	kgtypes.NodeProject,
	kgtypes.NodeTicket,
	kgtypes.NodePlan,
	kgtypes.NodeDocument,
	kgtypes.NodeResearch,
}

// InterceptQueryListProjects claims container-type listing queries.
// Returns (true, result) when the call shape matches; otherwise
// returns (false, _) so the chain continues.
func InterceptQueryListProjects(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// Only claim the bare-listing case. Text-search (a.Text != "")
	// belongs to the search compositor — let the chain fall through to
	// InterceptSearch or the server-side hybrid path.
	if a.Text != "" {
		return false, kgtools.ToolResult{}
	}
	// Type must be one of the container types — otherwise this is a
	// generic browse call and belongs to handleBrowse(JSON).
	if !isContainerListingType(a.Type) {
		return false, kgtools.ToolResult{}
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("list_projects: graph caller unavailable")
	}

	// Server-side handleListProjects's type-filter behavior: when
	// a.Type is set, restrict to that one type. Mirror exactly.
	types := containerTypesForListProjects
	if a.Type != "" {
		types = []kgtypes.NodeType{kgtypes.NodeType(a.Type)}
	}

	ctx := context.Background()
	var nodes []*knowledgev1.Node
	for _, t := range types {
		fetched, err := fetchNodesByType(ctx, gc, string(t))
		if err != nil {
			return true, errorResult("list_projects: " + err.Error())
		}
		nodes = append(nodes, fetched...)
	}

	if a.Format == "json" {
		// When fields is requested, project each node through the SAME projection
		// grammar the engine render arms use (engine.ProjectNodeJSON — tools→engine
		// import is one-way, no cycle), so the container-listing path honors the
		// tool-wide `fields` projection instead of marshaling raw []*knowledgev1.Node. An
		// empty fields list preserves the full-node marshal.
		if len(a.Fields) > 0 {
			rows := make([]map[string]any, len(nodes))
			for i, n := range nodes {
				rows[i] = engine.ProjectNodeJSON(n, a.Fields)
			}
			return true, jsonResult(map[string]any{
				"total": len(nodes),
				"nodes": rows,
			})
		}
		return true, jsonResult(map[string]any{
			"total": len(nodes),
			"nodes": nodes,
		})
	}
	return true, kgtools.TextResult(formatListProjectsMarkdown(nodes))
}

// isContainerListingType reports whether the type string matches one
// of the container types handleListProjects routes for.
func isContainerListingType(t string) bool {
	switch t {
	case "plan", "project", "ticket", "research", "document":
		return true
	}
	return false
}

// fetchNodesByType issues one query(type:T, limit:0) read via the Execute
// carrier seam and decodes the nodes_json carrier (engine.DecodeNodes). limit:0
// mirrors the server-side store.Match(t) with no Limit() — i.e. "everything".
func fetchNodesByType(ctx context.Context, gc GraphCaller, nodeType string) ([]*knowledgev1.Node, error) {
	args, err := json.Marshal(struct {
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}{Type: nodeType, Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("query(type=%s): %w", nodeType, err)
	}
	return engine.DecodeNodes(resp)
}

// extractWireText is the local mirror of toolResultText in
// cmd/knowledge/internal/projects/render/wire_fetch.go. Concatenates
// every text content block.
func extractWireText(r kgtools.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// formatListProjectsMarkdown is the byte-for-byte port of the server-
// side handleListProjects markdown render at
// cmd/knowledge-server/tools/tools_walk.go:242-259. Same separator
// shape (header + per-node block + trailing blank line) so the
// captured goldens remain byte-identical to the new client output.
func formatListProjectsMarkdown(nodes []*knowledgev1.Node) string {
	if len(nodes) == 0 {
		return "No projects or research documents found."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Projects & Research (%d):\n\n", len(nodes))
	for _, n := range nodes {
		status := ""
		if n.Status != "" {
			status = " [" + n.Status + "]"
		}
		fmt.Fprintf(&sb, "- %s (%s)%s\n", n.SymbolName, n.Type, status)
		if n.Description != "" {
			fmt.Fprintf(&sb, "  %s\n", truncate(n.Description, 100))
		}
		fmt.Fprintf(&sb, "  ID: %s\n\n", n.Id)
	}
	return sb.String()
}
