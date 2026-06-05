// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptWhatNext claims the what_next MCP call now
// that handleWhatNext was removed server-side. The implementation
// mirrors projects.WhatNext: for each candidate type (step + question,
// optionally project + ticket), list nodes of that type via query,
// filter to actionable status, check ancestry, dependencies, and emit
// either text or JSON.

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
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// whatNextArgs mirrors the wire shape for what_next.
type whatNextArgs struct {
	ProjectID         string `json:"project_id,omitempty"`
	IncludeContainers bool   `json:"include_containers,omitempty"`
	Verbose           bool   `json:"verbose,omitempty"`
	Format            string `json:"format,omitempty"`
}

// whatNextJSONStep mirrors the JSON shape captured in
// testdata/what_next_json.golden.
type whatNextJSONStep struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

type whatNextJSONResult struct {
	Total int                `json:"total"`
	Steps []whatNextJSONStep `json:"steps"`
}

// InterceptWhatNext handles the what_next MCP call.
func InterceptWhatNext(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "what_next" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("what_next: graph caller unavailable")
	}
	var a whatNextArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}

	ctx := context.Background()
	if a.ProjectID != "" {
		// Verify the project exists.
		node, lerr := LookupNode(ctx, gc, a.ProjectID)
		if lerr != nil || node == nil {
			return true, errorResult(fmt.Sprintf(
				"what_next: project %q not found (use query(type:\"project\") to list available projects)",
				a.ProjectID,
			))
		}
	}

	types := []kgtypes.NodeType{kgtypes.NodeStep, kgtypes.NodeQuestion}
	if a.IncludeContainers {
		types = append(types, kgtypes.NodeProject, kgtypes.NodeTicket)
	}

	candidates, lerr := collectActionableCandidates(ctx, gc, types, a.ProjectID)
	if lerr != nil {
		return true, errorResult("what_next: " + lerr.Error())
	}

	switch a.Format {
	case "json":
		return true, whatNextRenderJSON(candidates)
	case "ids":
		return true, whatNextRenderIDs(candidates)
	}
	if a.Verbose {
		return true, whatNextRenderVerbose(candidates)
	}
	return true, whatNextRenderConcise(ctx, gc, candidates)
}

// whatNextClientActionableState describes the ancestor state of a node
// in the project tree.
type whatNextClientActionableState struct {
	hasAncestor         bool
	hasTerminalAncestor bool
}

// collectActionableCandidates enumerates nodes of each candidate type
// via gc, runs the actionable / ancestor / deps filters, and returns
// the union.
func collectActionableCandidates(ctx context.Context, gc GraphCaller, types []kgtypes.NodeType, projectID string) ([]*knowledgev1.Node, error) {
	var candidates []*knowledgev1.Node
	for _, typ := range types {
		nodes, err := whatNextListByType(ctx, gc, typ)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if !isWhatNextCandidate(ctx, gc, n, projectID) {
				continue
			}
			candidates = append(candidates, n)
		}
	}
	return candidates, nil
}

// isWhatNextCandidate runs every filter (actionable, descendant,
// ancestor, deps) and returns true when the node qualifies.
func isWhatNextCandidate(ctx context.Context, gc GraphCaller, n *knowledgev1.Node, projectID string) bool {
	if !whatNextClientActionable(n) {
		return false
	}
	if projectID != "" && !whatNextClientIsDescendant(ctx, gc, n.Id, projectID) {
		return false
	}
	state := whatNextClientAncestorState(ctx, gc, n.Id)
	if !state.hasAncestor && kgtypes.NodeType(n.Type) != kgtypes.NodeProject && kgtypes.NodeType(n.Type) != kgtypes.NodeTicket {
		return false
	}
	if state.hasTerminalAncestor {
		return false
	}
	return whatNextClientAllDepsComplete(ctx, gc, n.Id)
}

// whatNextClientActionable mirrors projects.whatNextIsActionable.
func whatNextClientActionable(n *knowledgev1.Node) bool {
	switch kgtypes.NodeType(n.Type) {
	case kgtypes.NodeStep:
		return n.Status == kgtypes.StatusPending || n.Status == ""
	case kgtypes.NodeQuestion:
		return n.Status == "open" || n.Status == "investigating"
	case kgtypes.NodeProject:
		return n.Status == kgtypes.StatusActive || n.Status == ""
	case kgtypes.NodeTicket:
		return n.Status == kgtypes.StatusOpen || n.Status == kgtypes.StatusInProgress
	default:
		return false
	}
}

// whatNextClientTerminalStatus mirrors projects.whatNextIsTerminalStatus.
func whatNextClientTerminalStatus(status string) bool {
	switch status {
	case kgtypes.StatusCompleted, kgtypes.StatusSkipped, kgtypes.StatusArchived, kgtypes.StatusClosed, "superseded":
		return true
	}
	return false
}

// whatNextListByType issues a query(type:<typ>) read via the Execute carrier
// seam and returns the hydrated node list (engine.DecodeNodes nodes_json
// carrier). A transport-degraded fetch returns (nil, err); an empty graph
// returns (nil, nil).
func whatNextListByType(ctx context.Context, gc GraphCaller, typ kgtypes.NodeType) ([]*knowledgev1.Node, error) {
	args, err := json.Marshal(struct {
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}{Type: string(typ), Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("marshal list: %w", err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, err
	}
	return engine.DecodeNodes(resp)
}

// whatNextClientIsDescendant walks contains-edges backward up to 5
// hops looking for ancestorID.
func whatNextClientIsDescendant(ctx context.Context, gc GraphCaller, nodeID, ancestorID string) bool {
	current := nodeID
	for range 5 {
		edges, err := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains)
		if err != nil || len(edges) == 0 {
			return false
		}
		for _, e := range edges {
			if e.FromId == ancestorID {
				return true
			}
		}
		current = edges[0].FromId
	}
	return false
}

// whatNextClientAncestorState walks contains-edges backward up to 10
// hops and reports both whether the node has any ancestor and whether
// any ancestor on the path carries a terminal status.
func whatNextClientAncestorState(ctx context.Context, gc GraphCaller, nodeID string) whatNextClientActionableState {
	state := whatNextClientActionableState{}
	current := nodeID
	for range 10 {
		edges, err := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains)
		if err != nil || len(edges) == 0 {
			return state
		}
		state.hasAncestor = true
		parent, perr := render.FetchNode(ctx, gc, edges[0].FromId)
		if perr != nil || parent == nil {
			return state
		}
		if whatNextClientTerminalStatus(parent.Status) {
			state.hasTerminalAncestor = true
			return state
		}
		current = parent.Id
	}
	return state
}

// whatNextClientAllDepsComplete checks whether every depends-on target
// of nodeID is completed (or answered for questions).
func whatNextClientAllDepsComplete(ctx context.Context, gc GraphCaller, nodeID string) bool {
	edges, err := render.IterEdges(ctx, gc, nodeID, kgwire.OutgoingEdges, kgtypes.EdgeDependsOn)
	if err != nil || len(edges) == 0 {
		return true
	}
	for _, e := range edges {
		dep, derr := render.FetchNode(ctx, gc, e.ToId)
		if derr != nil || dep == nil {
			return false
		}
		if kgtypes.NodeType(dep.Type) == kgtypes.NodeQuestion {
			if dep.Status != "answered" {
				return false
			}
		} else {
			if dep.Status != kgtypes.StatusCompleted {
				return false
			}
		}
	}
	return true
}

// whatNextRenderConcise renders the concise text format. Mirrors the
// captured golden — "Next actionable steps (N):\n\n" header followed
// by enumerated rows.
func whatNextRenderConcise(ctx context.Context, gc GraphCaller, nodes []*knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Next actionable steps (%d):\n\n", len(nodes))
	for i, n := range nodes {
		fmt.Fprintf(&sb, "%d. %s [%s] %s — id:%s\n", i+1, n.SymbolName, n.Type, n.Status, n.Id)
		// "under: <parent>" line lifted from the captured golden — find
		// the first contains-edge ancestor and emit its name.
		edges, _ := render.IterEdges(ctx, gc, n.Id, kgwire.IncomingEdges, kgtypes.EdgeKGContains)
		if len(edges) > 0 {
			parent, _ := render.FetchNode(ctx, gc, edges[0].FromId)
			if parent != nil {
				fmt.Fprintf(&sb, "   under: %s (%s)\n", parent.SymbolName, parent.Type)
			}
		}
	}
	return textResult(sb.String())
}

// whatNextRenderVerbose renders the verbose text format. Mirrors the
// captured golden — enumerated rows with description + ID.
func whatNextRenderVerbose(nodes []*knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Next actionable steps (%d):\n\n", len(nodes))
	for i, n := range nodes {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, n.SymbolName)
		if n.Description != "" {
			fmt.Fprintf(&sb, "   %s\n", n.Description)
		}
		fmt.Fprintf(&sb, "   ID: %s\n\n", n.Id)
	}
	return textResult(sb.String())
}

// whatNextRenderJSON emits the structured JSON result.
func whatNextRenderJSON(nodes []*knowledgev1.Node) kgtools.ToolResult {
	out := whatNextJSONResult{Total: len(nodes), Steps: make([]whatNextJSONStep, 0, len(nodes))}
	for _, n := range nodes {
		out.Steps = append(out.Steps, whatNextJSONStep{
			ID:          n.Id,
			Name:        n.SymbolName,
			Type:        n.Type,
			Status:      n.Status,
			Description: n.Description,
		})
	}
	return jsonResult(out)
}

// whatNextRenderIDs emits one ID per line.
func whatNextRenderIDs(nodes []*knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	for i, n := range nodes {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(n.Id)
	}
	return textResult(sb.String())
}
