// SPDX-License-Identifier: Apache-2.0

// Package tools — "explain this correlation" mode for log graphs.
//
// `query({ graph: "logs", name: "<id>", mode: "explain", id: "<tplA>" })`
// returns a per-correlation explanation: edge metadata (Confidence,
// Method, Evidence), parsed services/resources, both templates' time
// windows, the overlap intersection, and the contribution of each
// dimension to the score. Helps the agent answer "why is this score
// 0.99 vs 0.19" without re-running the pipeline's dependency checker.
//
// API shapes:
//   - id only: explain every correlation involving the given template
//   - extra={"a": "<tplA-id-or-alias>", "b": "<tplB-id-or-alias>"}:
//     explain just the specific pair
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// explainRow holds the resolved data for a single correlation
// explanation. Differs from correlationRow by carrying both endpoint
// templates as resolved logwire.LogTemplate pointers (alias-friendly
// rendering) and the full edge struct (so Method/LastValidated etc.
// can be surfaced).
type explainRow struct {
	A, B      *logwire.LogTemplate
	Edge      *knowledgev1.Edge
	ServiceA  string
	ServiceB  string
	ResourceA string
	ResourceB string
}

// handleLogsExplain is the graph='logs' mode='explain' entry point.
// Resolves the template pair (or single anchor template + all peers)
// and renders one explanation block per correlation.
func handleLogsExplain(
	queryID string, engine *logs.QueryEngine, st *logState,
	idAnchor string, extra map[string]string,
) kgtools.ToolResult {
	if st == nil {
		return kgtools.ErrorResult(fmt.Sprintf(
			"logs explain %q: no pre-fetched log state — cannot read CORRELATES_WITH edges",
			queryID))
	}
	rows, err := buildExplainRows(engine, st, idAnchor, extra)
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("logs explain %q: %s", queryID, err.Error()))
	}
	return kgtools.TextResult(formatExplain(queryID, rows))
}

// buildExplainRows resolves the input shape (single id vs. a/b pair)
// into a slice of explainRow ready for rendering.
func buildExplainRows(
	engine *logs.QueryEngine, st *logState,
	idAnchor string, extra map[string]string,
) ([]explainRow, error) {
	a := strings.TrimSpace(extra["a"])
	b := strings.TrimSpace(extra["b"])
	switch {
	case a != "" && b != "":
		return explainSpecificPair(engine, st, a, b)
	case idAnchor != "":
		return explainAllFromAnchor(engine, st, idAnchor)
	case a != "":
		return explainAllFromAnchor(engine, st, a)
	default:
		return nil, fmt.Errorf(
			"explain mode requires id=<template> OR extra={a:<tpl>, b:<tpl>}")
	}
}

// explainSpecificPair finds the CORRELATES_WITH edge between two
// named templates (in either direction) and returns one row.
func explainSpecificPair(
	engine *logs.QueryEngine, st *logState,
	aRef, bRef string,
) ([]explainRow, error) {
	tplA, err := resolveTemplate(engine, aRef)
	if err != nil {
		return nil, fmt.Errorf("template a=%q: %w", aRef, err)
	}
	tplB, err := resolveTemplate(engine, bRef)
	if err != nil {
		return nil, fmt.Errorf("template b=%q: %w", bRef, err)
	}
	edge, ok := findCorrelationEdge(st, tplA.ID, tplB.ID)
	if !ok {
		return nil, fmt.Errorf(
			"no CORRELATES_WITH edge between %s and %s", tplA.ID, tplB.ID)
	}
	row := explainRowFromEdge(edge, tplA, tplB)
	return []explainRow{row}, nil
}

// explainAllFromAnchor returns one row per CORRELATES_WITH edge that
// involves the anchor template (both directions). Useful for "what's
// this template correlated with, and why each."
func explainAllFromAnchor(
	engine *logs.QueryEngine, st *logState, anchorRef string,
) ([]explainRow, error) {
	tplAnchor, err := resolveTemplate(engine, anchorRef)
	if err != nil {
		return nil, fmt.Errorf("anchor %q: %w", anchorRef, err)
	}
	var rows []explainRow
	for _, dir := range []kgwire.EdgeDirection{kgwire.OutgoingEdges, kgwire.IncomingEdges} {
		edges := st.EdgesOf(tplAnchor.ID, dir, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
		for i := range edges {
			e := &edges[i]
			peerID := e.ToId
			if dir == kgwire.IncomingEdges {
				peerID = e.FromId
			}
			peer := templateByID(engine, peerID)
			if dir == kgwire.OutgoingEdges {
				rows = append(rows, explainRowFromEdge(e, tplAnchor, peer))
				continue
			}
			rows = append(rows, explainRowFromEdge(e, peer, tplAnchor))
		}
	}
	return rows, nil
}

// resolveTemplate accepts an alias OR a template ID and returns the
// matching LogTemplate. Errors when neither resolves.
func resolveTemplate(engine *logs.QueryEngine, ref string) (*logwire.LogTemplate, error) {
	id, ok := engine.ResolveTemplateID(ref)
	if !ok {
		id = ref
	}
	for _, t := range engine.Templates() {
		if t != nil && t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("template not found: %q", ref)
}

// findCorrelationEdge looks for an EdgeCorrelatesWith between the two
// template IDs in either direction. Returns the first match or ok=false.
func findCorrelationEdge(st *logState, a, b string) (*knowledgev1.Edge, bool) {
	for _, dir := range []kgwire.EdgeDirection{kgwire.OutgoingEdges, kgwire.IncomingEdges} {
		edges := st.EdgesOf(a, dir, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
		for i := range edges {
			e := &edges[i]
			peer := e.ToId
			if dir == kgwire.IncomingEdges {
				peer = e.FromId
			}
			if peer == b {
				return e, true
			}
		}
	}
	return nil, false
}

// explainRowFromEdge populates an explainRow from the parsed edge.
func explainRowFromEdge(e *knowledgev1.Edge, a, b *logwire.LogTemplate) explainRow {
	row := explainRow{A: a, B: b, Edge: e}
	row.ServiceA, row.ServiceB, row.ResourceA, row.ResourceB =
		parseCorrelationEvidence(e.Evidence)
	return row
}

// formatExplain renders the explanation as a sequence of blocks (one
// per row). Each block shows the pair, the score, the parsed evidence,
// the time windows, and the overlap.
func formatExplain(queryID string, rows []explainRow) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Correlation explanation — %s\n\n", queryID)
	if len(rows) == 0 {
		sb.WriteString("_No correlations to explain._\n")
		return sb.String()
	}
	for i, r := range rows {
		writeExplainBlock(&sb, r, i+1)
	}
	return sb.String()
}

// writeExplainBlock renders one correlation explanation. Sections:
// header, identifiers, edge metadata, time windows, overlap, services.
func writeExplainBlock(sb *strings.Builder, r explainRow, idx int) {
	fmt.Fprintf(sb, "### #%d — %s ↔ %s\n\n",
		idx, templateRefAlias(r.A), templateRefAlias(r.B))
	writeExplainHeaders(sb, r)
	writeExplainEdge(sb, r)
	writeExplainTimes(sb, r)
	writeExplainServices(sb, r)
	sb.WriteString("\n")
}

// writeExplainHeaders renders the patterns of the two templates so
// the agent doesn't have to cross-reference IDs.
func writeExplainHeaders(sb *strings.Builder, r explainRow) {
	if r.A != nil && r.A.Pattern != "" {
		fmt.Fprintf(sb, "- A pattern: `%s`\n", r.A.Pattern)
	}
	if r.B != nil && r.B.Pattern != "" {
		fmt.Fprintf(sb, "- B pattern: `%s`\n", r.B.Pattern)
	}
}

// writeExplainEdge renders the edge's score + method + raw evidence
// fields (each conditional on being non-empty).
func writeExplainEdge(sb *strings.Builder, r explainRow) {
	fmt.Fprintf(sb, "- Score: **%.2f**", r.Edge.Confidence)
	if r.Edge.Method != "" {
		fmt.Fprintf(sb, " (method=%s)", r.Edge.Method)
	}
	sb.WriteString("\n")
	if r.Edge.LastValidated != 0 {
		fmt.Fprintf(sb, "- Last validated: %s\n",
			time.Unix(0, r.Edge.LastValidated).Format("2006-01-02T15:04:05Z07:00"))
	}
	if r.Edge.Evidence != "" {
		fmt.Fprintf(sb, "- Evidence (raw): `%s`\n", r.Edge.Evidence)
	}
}

// writeExplainTimes renders both template windows + the overlap
// intersection in a compact line.
func writeExplainTimes(sb *strings.Builder, r explainRow) {
	if r.A != nil {
		fmt.Fprintf(sb, "- A window: %s\n", formatRange(r.A.FirstSeen, r.A.LastSeen))
	}
	if r.B != nil {
		fmt.Fprintf(sb, "- B window: %s\n", formatRange(r.B.FirstSeen, r.B.LastSeen))
	}
	lo, hi := overlapWindow(r.A, r.B)
	fmt.Fprintf(sb, "- Overlap: %s\n", formatRange(lo, hi))
}

// writeExplainServices renders the parsed-from-evidence service +
// resource pair so the agent sees the cloud context that fed into the
// dependency check.
func writeExplainServices(sb *strings.Builder, r explainRow) {
	if r.ServiceA != "" || r.ServiceB != "" {
		fmt.Fprintf(sb, "- Services: `%s` ↔ `%s`\n",
			serviceOrDash(r.ServiceA), serviceOrDash(r.ServiceB))
	}
	if r.ResourceA != "" || r.ResourceB != "" {
		fmt.Fprintf(sb, "- Resources: `%s` ↔ `%s`\n",
			serviceOrDash(r.ResourceA), serviceOrDash(r.ResourceB))
	}
}

// templateRefAlias renders a template's alias when set, else its
// short hex ID. Used in the per-block header.
func templateRefAlias(t *logwire.LogTemplate) string {
	if t == nil {
		return "_unknown_"
	}
	if t.Alias != "" {
		return fmt.Sprintf("`%s`", t.Alias)
	}
	return fmt.Sprintf("`%s`", shortHex(t.ID))
}

// formatRange renders a time-window pair compactly, with span. Returns
// "n/a" when either bound is zero.
func formatRange(lo, hi time.Time) string {
	if lo.IsZero() || hi.IsZero() {
		return "n/a"
	}
	return fmt.Sprintf("%s → %s (%s)",
		lo.Format("15:04:05"),
		hi.Format("15:04:05"),
		hi.Sub(lo).Truncate(time.Second))
}
