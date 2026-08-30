// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryEvidence serves query(mode:"evidence")
// client-side. It began as a port of the server-side handleEvidenceFor
// handler; that handler and the file that held it are both gone —
// verified repo-wide by symbol and by path — and no server-side
// evidence arm replaced them, so this intercept is the whole
// implementation.
//
// Walks the informed-by → references chain from a decision via the
// existing render.FetchNode + render.IterEdges wire helpers. Both
// markdown and JSON formats are supported with byte-parity goldens
// (testdata/evidence.golden, testdata/evidence.json.golden).

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
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// InterceptQueryEvidence claims query(mode:"evidence"). Returns
// (true, result) on match.
func InterceptQueryEvidence(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "evidence" {
		return false, kgtools.ToolResult{}
	}
	if a.ID == "" {
		return true, errorResult("evidence mode requires 'id' parameter (the decision ID)")
	}

	if err := accountQueryParams(armEvidence, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("evidence: graph caller unavailable")
	}

	decision, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil {
		return true, errorResult(fmt.Sprintf("query failed: %s", err))
	}
	if decision == nil {
		return true, errorResult(fmt.Sprintf("node %s not found", a.ID))
	}

	if a.Format == "json" {
		return true, jsonResult(buildEvidenceJSON(ctx, gc, decision))
	}
	return true, kgtools.TextResult(renderEvidenceMarkdown(ctx, gc, decision))
}

// renderEvidenceMarkdown is pure formatting over wire-fetched edges +
// nodes: the decision header, then one bullet per informed-by evidence
// node, then its references indented beneath.
//
// PROVENANCE, NOT A POINTER: this wording was ported byte-for-byte from
// the markdown branch of the server-side handleEvidenceFor, which no
// longer exists by symbol or by path. WHAT HOLDS THE FORMAT NOW is
// TestInterceptQueryEvidence_TextFormat_ByteIdentical against
// testdata/evidence.golden — that assertion, not the vanished port
// source, is the standing contract.
func renderEvidenceMarkdown(ctx context.Context, gc GraphCaller, decision *knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Decision: %s\n", decision.SymbolName)
	fmt.Fprintf(&sb, "Choice: %s\n", kgtypes.Value(decision, "choice"))
	fmt.Fprintf(&sb, "Rationale: %s\n", kgtypes.Value(decision, "rationale"))
	if alts := kgtypes.Value(decision, "alternatives"); alts != "" {
		fmt.Fprintf(&sb, "Alternatives: %s\n", alts)
	}

	evEdges, _ := render.IterEdges(ctx, gc, decision.Id, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	if len(evEdges) > 0 {
		sb.WriteString("\nEvidence:\n")
		for _, e := range evEdges {
			eNode, ferr := render.FetchNode(ctx, gc, e.ToId)
			if ferr != nil || eNode == nil {
				continue
			}
			fmt.Fprintf(&sb, "  • [%s] %s\n    %s\n", eNode.Type, eNode.SymbolName, eNode.Summary)
			refEdges, _ := render.IterEdges(ctx, gc, e.ToId, kgwire.OutgoingEdges, kgtypes.EdgeReferences)
			for _, re := range refEdges {
				refNode, rerr := render.FetchNode(ctx, gc, re.ToId)
				if rerr != nil || refNode == nil {
					continue
				}
				refType := kgtypes.Value(refNode, "type")
				switch refType {
				case "url":
					fmt.Fprintf(&sb, "    → %s: %s\n", refNode.SymbolName, kgtypes.Value(refNode, "url"))
				case "file":
					fmt.Fprintf(&sb, "    → %s: %s\n", refNode.SymbolName, kgtypes.Value(refNode, "file"))
				default:
					fmt.Fprintf(&sb, "    → %s (%s)\n", refNode.SymbolName, refNode.Type)
				}
			}
		}
	}
	return sb.String()
}

// evidenceRefRow / evidenceRow are the JSON payload shape for
// query(mode:"evidence", format:"json"). They began as named copies of
// two anonymous structs declared inside the since-deleted server-side
// buildEvidenceJSON; that file is gone by path and those types with it.
//
// WHAT PINS THE SHAPE NOW is testdata/evidence.json.golden, asserted
// byte-for-byte by TestInterceptQueryEvidence_JSONFormat_ByteIdentical.
// Note the reach of that golden: its fixture emits one url-typed
// reference per finding, so it pins the field set and field ORDER of
// the fully-populated form. The refType "file" and default branches,
// and the omitted form of the three omitempty fields, have no fixture
// in this package and are therefore unpinned.
type evidenceRefRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

type evidenceRow struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Name       string           `json:"name"`
	Summary    string           `json:"summary,omitempty"`
	References []evidenceRefRow `json:"references,omitempty"`
}

// buildEvidenceJSON assembles the evidence payload: the decision header,
// then one row per informed-by node with its references nested.
//
// PROVENANCE, NOT A POINTER: it is a port of a server-side function of
// the same name, which is gone by symbol and by path along with the file
// that held it. The one substitution the port made is the reason the two
// bodies are not identical — every direct store query became a wire call
// through render.FetchNode (node by ID) and render.IterEdges (edge walk),
// because a client-side intercept has no store handle.
func buildEvidenceJSON(ctx context.Context, gc GraphCaller, decision *knowledgev1.Node) map[string]any {
	out := map[string]any{
		"decision": map[string]any{
			"id":           decision.Id,
			"name":         decision.SymbolName,
			"choice":       kgtypes.Value(decision, "choice"),
			"rationale":    kgtypes.Value(decision, "rationale"),
			"alternatives": kgtypes.Value(decision, "alternatives"),
		},
	}
	evEdges, _ := render.IterEdges(ctx, gc, decision.Id, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	evidence := make([]evidenceRow, 0, len(evEdges))
	for _, e := range evEdges {
		eNode, ferr := render.FetchNode(ctx, gc, e.ToId)
		if ferr != nil || eNode == nil {
			continue
		}
		row := evidenceRow{ID: eNode.Id, Type: eNode.Type, Name: eNode.SymbolName, Summary: eNode.Summary}
		refEdges, _ := render.IterEdges(ctx, gc, e.ToId, kgwire.OutgoingEdges, kgtypes.EdgeReferences)
		for _, re := range refEdges {
			refNode, rerr := render.FetchNode(ctx, gc, re.ToId)
			if rerr != nil || refNode == nil {
				continue
			}
			rt := kgtypes.Value(refNode, "type")
			val := ""
			switch rt {
			case "url":
				val = kgtypes.Value(refNode, "url")
			case "file":
				val = kgtypes.Value(refNode, "file")
			}
			row.References = append(row.References, evidenceRefRow{
				ID: refNode.Id, Name: refNode.SymbolName, Type: refNode.Type, Value: val,
			})
		}
		evidence = append(evidence, row)
	}
	out["evidence"] = evidence
	return out
}
