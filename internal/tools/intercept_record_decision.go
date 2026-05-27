// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptRecordDecision claims the record_decision MCP
// call. Builds a decision node + informed-by edges in a single
// PersistBatch (validating each informed_by reference exists with a
// per-ref LookupNode call first).

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// recordDecisionArgs mirrors the wire shape for record_decision.
type recordDecisionArgs struct {
	Name         string `json:"name"`
	Choice       string `json:"choice"`
	Rationale    string `json:"rationale"`
	Alternatives string `json:"alternatives,omitempty"`
	InformedBy   string `json:"informed_by,omitempty"`
	Format       string `json:"format,omitempty"`
}

// recordDecisionJSONResult mirrors the JSON shape the captured golden
// expects: {id, name, warnings}.
type recordDecisionJSONResult struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Warnings []string `json:"warnings"`
}

// InterceptRecordDecision handles the record_decision MCP call.
func InterceptRecordDecision(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "record_decision" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("record_decision: graph caller unavailable")
	}
	var a recordDecisionArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
	if err := validate.Name("record_decision", a.Name); err != nil {
		return true, errorResult(err.Error())
	}
	if strings.TrimSpace(a.Choice) == "" {
		return true, errorResult("record_decision: choice is required and must be non-empty (what was decided)")
	}

	ctx := context.Background()
	node := buildDecisionNode(a)
	warnings, validRefs := validateInformedByRefs(ctx, gc, a.InformedBy)

	nodes := []*knowledgev1.Node{node}
	var edges []kgwire.BatchEdge
	for _, ref := range validRefs {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: ref, Type: kgtypes.EdgeInformedBy})
	}
	ids, perr := PersistBatch(ctx, gc, nodes, edges, newBundleID())
	if perr != nil {
		return true, errorResult("record decision: " + perr.Error())
	}
	if len(ids) == 0 {
		return true, errorResult("record decision: persist returned no IDs")
	}
	id := ids[0]
	if a.Format == "json" {
		return true, jsonResult(recordDecisionJSONResult{ID: id, Name: a.Name, Warnings: warnings})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Decision recorded: %s → ID: %s", a.Name, id)
	writeClientWarningsSection(&sb, warnings, "\n\n")
	return true, textResult(sb.String() + " [graph: knowledge/default]")
}

// buildDecisionNode constructs the decision node with metadata.
// Mirrors projects.RecordDecision body.
func buildDecisionNode(a recordDecisionArgs) *knowledgev1.Node {
	summary := a.Choice + " because " + a.Rationale
	if a.Alternatives != "" {
		summary += ". Alternatives considered: " + a.Alternatives
	}
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeDecision),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Choice,
		Summary:     summary,
	}
	kgtypes.SetValue(node, "choice", a.Choice)
	kgtypes.SetValue(node, "rationale", a.Rationale)
	if a.Alternatives != "" {
		kgtypes.SetValue(node, "alternatives", a.Alternatives)
	}
	return node
}

// validateInformedByRefs splits the comma-separated informed_by list
// and probes each ID via LookupNode. Returns parallel warning + valid-
// ref slices (mirrors projects.RecordDecision behavior).
func validateInformedByRefs(ctx context.Context, gc GraphCaller, informedBy string) (warnings, validRefs []string) {
	if informedBy == "" {
		return nil, nil
	}
	for ref := range strings.SplitSeq(informedBy, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		n, err := LookupNode(ctx, gc, ref)
		if err != nil || n == nil {
			warnings = append(warnings, fmt.Sprintf("informed_by id %q not found in any indexed graph — skipped", ref))
			continue
		}
		validRefs = append(validRefs, ref)
	}
	return warnings, validRefs
}
