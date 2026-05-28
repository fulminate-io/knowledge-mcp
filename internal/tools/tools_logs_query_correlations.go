// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph correlations retrieval mode.
//
// `query({ graph: "logs", name: "<id>", mode: "correlations" })` returns
// every CORRELATES_WITH edge that was written during collect, sorted by
// confidence (score) descending. Each row resolves the template pair to
// aliases + patterns, extracts the service/resource context from the
// edge's Evidence field, and emits the overlapping time window computed
// from each template's FirstSeen / LastSeen.
//
// The collect tool's text summary shows these pairs once and discards
// them; this mode is the persisted retrieval path so an agent can
// revisit the structural answer any time after collect finishes.
package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// correlationRow is a single rendered correlation: the edge + resolved
// template metadata + parsed evidence fields. Sorting is always by Score
// desc (ties broken alphabetically on TemplateA.Alias for stability).
type correlationRow struct {
	TemplateA *logwire.LogTemplate
	TemplateB *logwire.LogTemplate
	Score     float64
	ServiceA  string
	ServiceB  string
	ResourceA string
	ResourceB string
	Method    string
	OverlapLo time.Time
	OverlapHi time.Time
}

// handleLogsCorrelations iterates every template's outgoing
// CORRELATES_WITH edges, parses each edge's Evidence field into its
// original service/resource components, computes the temporal overlap
// from each template's FirstSeen/LastSeen, and renders a markdown
// table sorted by score descending. Returns a friendly "no correlations"
// message when the log graph has none.
func handleLogsCorrelations(
	queryID string,
	engine *logs.QueryEngine,
	st *logState,
) kgtools.ToolResult {
	if st == nil {
		return kgtools.ErrorResult(fmt.Sprintf(
			"logs correlations %q: no pre-fetched log state — cannot read CORRELATES_WITH edges",
			queryID))
	}
	rows := collectCorrelationRows(engine, st)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return templateAliasOrID(rows[i].TemplateA) < templateAliasOrID(rows[j].TemplateA)
	})
	return kgtools.TextResult(formatCorrelations(queryID, rows))
}

// collectCorrelationRows walks every indexed template, iterates its
// outgoing CORRELATES_WITH edges, and assembles a correlationRow per
// edge. Outgoing-only iteration is sufficient because the pipeline
// writes exactly one directed edge per correlation (FromID=TemplateA,
// ToID=TemplateB) — the symmetric pair is implicit.
func collectCorrelationRows(
	engine *logs.QueryEngine,
	st *logState,
) []correlationRow {
	var rows []correlationRow
	for _, tmplA := range engine.Templates() {
		if tmplA == nil {
			continue
		}
		edges := st.EdgesOf(tmplA.ID, kgwire.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
		for i := range edges {
			e := &edges[i]
			tmplB := templateByID(engine, e.ToId)
			rows = append(rows, buildCorrelationRow(tmplA, tmplB, e))
		}
	}
	return rows
}

// templateByID looks up a template by hex ID against the engine. Falls
// back to a bare stub when the engine doesn't know the ID — shouldn't
// happen in practice but keeps rendering robust against stale edges.
func templateByID(engine *logs.QueryEngine, id string) *logwire.LogTemplate {
	for _, t := range engine.Templates() {
		if t != nil && t.ID == id {
			return t
		}
	}
	return &logwire.LogTemplate{ID: id}
}

// buildCorrelationRow fills a correlationRow from its template pair
// plus the raw edge. Evidence is parsed if present; time overlap is
// computed from each template's FirstSeen/LastSeen.
func buildCorrelationRow(tmplA, tmplB *logwire.LogTemplate, e *knowledgev1.Edge) correlationRow {
	row := correlationRow{
		TemplateA: tmplA,
		TemplateB: tmplB,
		Score:     e.Confidence,
		Method:    e.Method,
	}
	svcA, svcB, resA, resB := parseCorrelationEvidence(e.Evidence)
	row.ServiceA = svcA
	row.ServiceB = svcB
	row.ResourceA = resA
	row.ResourceB = resB
	row.OverlapLo, row.OverlapHi = overlapWindow(tmplA, tmplB)
	return row
}

// parseCorrelationEvidence pulls the four named fields out of the
// Evidence string produced by logs.writeCorrelations:
//
//	"services=A,B resources=X,Y score=0.92"
//
// The field order is fixed so a tolerant split-on-space + prefix match
// is enough. Missing fields return empty strings rather than erroring.
func parseCorrelationEvidence(evidence string) (svcA, svcB, resA, resB string) {
	for tok := range strings.FieldsSeq(evidence) {
		switch {
		case strings.HasPrefix(tok, "services="):
			svcA, svcB = splitPair(strings.TrimPrefix(tok, "services="))
		case strings.HasPrefix(tok, "resources="):
			resA, resB = splitPair(strings.TrimPrefix(tok, "resources="))
		}
	}
	return svcA, svcB, resA, resB
}

// splitPair splits "A,B" into ("A", "B"). An empty or single-value
// input yields ("", "") / ("A", "") — parseCorrelationEvidence is the
// only caller and both degraded shapes render sensibly.
func splitPair(s string) (string, string) {
	left, right, ok := strings.Cut(s, ",")
	if !ok {
		return left, ""
	}
	return left, right
}

// overlapWindow returns the intersection of each template's FirstSeen
// / LastSeen range. If either side has a zero boundary (legacy graphs
// that didn't record timestamps) the returned window is zero-valued
// and the formatter renders "n/a".
func overlapWindow(a, b *logwire.LogTemplate) (time.Time, time.Time) {
	if a == nil || b == nil {
		return time.Time{}, time.Time{}
	}
	if a.FirstSeen.IsZero() || a.LastSeen.IsZero() ||
		b.FirstSeen.IsZero() || b.LastSeen.IsZero() {
		return time.Time{}, time.Time{}
	}
	lo := a.FirstSeen
	if b.FirstSeen.After(lo) {
		lo = b.FirstSeen
	}
	hi := a.LastSeen
	if b.LastSeen.Before(hi) {
		hi = b.LastSeen
	}
	if lo.After(hi) {
		return time.Time{}, time.Time{}
	}
	return lo, hi
}

// templateAliasOrID returns the template's alias if set, else its
// short hex ID. Used as a stable secondary sort key.
func templateAliasOrID(t *logwire.LogTemplate) string {
	if t == nil {
		return ""
	}
	if t.Alias != "" {
		return t.Alias
	}
	return t.ID
}

// formatCorrelations renders the correlation list as a markdown table.
// Columns: template A, template B, services, score, overlap window.
// Long patterns are shown as a second-line snippet under each template
// reference so the row stays narrow.
func formatCorrelations(queryID string, rows []correlationRow) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log correlations — %s\n\n", queryID)
	if len(rows) == 0 {
		sb.WriteString("_No CORRELATES_WITH edges in this log graph._ ")
		sb.WriteString("Correlations are only written during `collect` when temporal+structural ")
		sb.WriteString("co-occurrence is confirmed via cloud dependency.\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "%d confirmed correlation(s), sorted by score desc.\n\n", len(rows))
	sb.WriteString("| template A | template B | services | score | overlap |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "| %s | %s | %s | %.2f | %s |\n",
			renderTemplateRef(r.TemplateA),
			renderTemplateRef(r.TemplateB),
			renderServicePair(r),
			r.Score,
			renderOverlap(r.OverlapLo, r.OverlapHi),
		)
	}
	sb.WriteString("\nTraverse a template for its streams + chunks: ")
	sb.WriteString("`traverse({ graph: \"logs\", name: \"")
	sb.WriteString(queryID)
	sb.WriteString("\", start: \"<alias>\", direction: \"down\" })`\n")
	return sb.String()
}

// renderTemplateRef renders a template as "`alias` — pattern" or just
// "`id`" when no alias is known. Kept short so the table row is
// readable on a single line.
func renderTemplateRef(t *logwire.LogTemplate) string {
	if t == nil {
		return "_unknown_"
	}
	alias := t.Alias
	if alias == "" {
		alias = shortHex(t.ID)
	}
	if t.Pattern == "" {
		return fmt.Sprintf("`%s`", alias)
	}
	pattern := t.Pattern
	if len(pattern) > 60 {
		pattern = pattern[:60] + "…"
	}
	return fmt.Sprintf("`%s` — %s", alias, pattern)
}

// renderServicePair formats the service pair, falling back to the
// resource identifiers when services are empty (Evidence degraded).
func renderServicePair(r correlationRow) string {
	a, b := r.ServiceA, r.ServiceB
	if a == "" && b == "" {
		a, b = r.ResourceA, r.ResourceB
	}
	if a == "" && b == "" {
		return "_unknown_"
	}
	return fmt.Sprintf("%s ↔ %s", a, b)
}

// renderOverlap formats the overlap window as
// "HH:MM:SS–HH:MM:SS (Ns)". Returns "n/a" for the zero window.
func renderOverlap(lo, hi time.Time) string {
	if lo.IsZero() || hi.IsZero() {
		return "n/a"
	}
	span := hi.Sub(lo)
	return fmt.Sprintf("%s–%s (%s)",
		lo.Format("15:04:05"),
		hi.Format("15:04:05"),
		span.Truncate(time.Second))
}

// shortHexLen is the canonical short-hash width used wherever an
// alias is missing — long enough to disambiguate in practice (the
// 12-vs-8 tradeoff settled in favor of 8 during the alias UX work).
const shortHexLen = 8

// shortHex returns the first shortHexLen chars of a hex-looking
// string, or the whole thing when shorter. Used for template-ID
// fallback when no alias is set.
func shortHex(s string) string {
	if len(s) <= shortHexLen {
		return s
	}
	return s[:shortHexLen]
}
