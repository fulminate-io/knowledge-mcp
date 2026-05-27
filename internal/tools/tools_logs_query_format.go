// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph query result formatting.
//
// Split out from tools_logs_query.go so the dispatcher file stays under the
// 300-line soft cap. The three render functions here are the only callers
// aware of markdown layout — the dispatcher just decides which of them to
// invoke.
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// handleLogsOverview renders the overview view: a Top-Templates section
// ranked by signal strength followed by per-label distributions. The
// top-templates section is the headline an agent reads first; the
// label tables are kept underneath for drill-down navigation.
func handleLogsOverview(
	queryID string, engine *logs.QueryEngine, st *logState,
) kgtools.ToolResult {
	overview := engine.Overview()
	if len(overview) == 0 && engine.TemplateCount() == 0 {
		return kgtools.TextResult(fmt.Sprintf("Log graph %q has no indexed label values yet.", queryID))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log overview — %s\n\n", queryID)
	fmt.Fprintf(&sb, "%d stream(s), %d template(s)\n\n",
		engine.StreamCount(), engine.TemplateCount())

	ranked := rankTemplatesBySignal(st, engine.Templates())
	writeTopTemplatesSection(&sb, ranked, 10)
	writeOverviewLabelSections(&sb, overview)
	return kgtools.TextResult(sb.String())
}

// writeOverviewLabelSections renders the per-label tables that have
// always been part of the overview. Extracted so handleLogsOverview
// stays focused on top-level layout.
func writeOverviewLabelSections(sb *strings.Builder, overview map[string][]logs.LabelValueRanked) {
	keys := make([]string, 0, len(overview))
	for k := range overview {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ranked := overview[k]
		if len(ranked) == 0 {
			continue
		}
		fmt.Fprintf(sb, "### %s\n\n", k)
		sb.WriteString("| value | errors | warns | info | total |\n")
		sb.WriteString("|-------|--------|-------|------|-------|\n")
		for _, row := range ranked {
			if row.Stats == nil {
				continue
			}
			fmt.Fprintf(sb, "| %s | %d | %d | %d | %d |\n",
				row.Value, row.Stats.ErrorCount, row.Stats.WarnCount,
				row.Stats.InfoCount, row.Stats.TotalCount)
		}
		sb.WriteString("\n")
	}
}

// handleLogsDrillDown parses a filter expression, intersects label filters
// and severity range against the LabelIndex, and renders the matching
// streams along with the templates they reference. Templates are sorted
// by signal score (count × correlations × severity × recency) so the
// dominant pattern surfaces first.
func handleLogsDrillDown(
	queryID string, engine *logs.QueryEngine, st *logState, text string,
) kgtools.ToolResult {
	labelFilters, minSev, err := parseLogFilters(text)
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("parse filter %q: %s", text, err.Error()))
	}

	streams := intersectLogStreams(engine, labelFilters, minSev)
	templates := resolveTemplatesForStreams(engine, streams)
	sortTemplatesBySignal(st, templates)

	if len(streams) == 0 {
		return kgtools.TextResult(fmt.Sprintf(
			"No streams match %q in log graph %q.",
			text, queryID,
		))
	}
	return kgtools.TextResult(formatLogsDrillDown(queryID, text, labelFilters, minSev, streams, templates, engine))
}

// intersectLogStreams combines label filters (AND) and a severity range
// into a single stream list. Filtering is done in-memory after each
// index probe to keep the logic linear in the result size.
func intersectLogStreams(
	engine *logs.QueryEngine,
	labelFilters map[string]string,
	minSev string,
) []*logwire.LogStream {
	var byLabel, bySev []*logwire.LogStream
	if len(labelFilters) > 0 {
		byLabel = engine.QueryLabels(labelFilters)
	}
	if minSev != "" {
		bySev = engine.QuerySeverityRange(minSev)
	}
	switch {
	case len(labelFilters) > 0 && minSev != "":
		return intersectStreamLists(byLabel, bySev)
	case len(labelFilters) > 0:
		return byLabel
	case minSev != "":
		return bySev
	default:
		return nil
	}
}

// intersectStreamLists returns the streams present in both lists,
// preserving order from the first list.
func intersectStreamLists(a, b []*logwire.LogStream) []*logwire.LogStream {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	idx := make(map[string]struct{}, len(b))
	for _, s := range b {
		if s != nil {
			idx[s.ID] = struct{}{}
		}
	}
	out := make([]*logwire.LogStream, 0, len(a))
	for _, s := range a {
		if s == nil {
			continue
		}
		if _, ok := idx[s.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}

// resolveTemplatesForStreams collects the templates referenced by the given
// streams (via LabelsForTemplate/QueryLabels inputs) for display.
func resolveTemplatesForStreams(engine *logs.QueryEngine, streams []*logwire.LogStream) []*logwire.LogTemplate {
	if len(streams) == 0 {
		return nil
	}
	// Use the exposed TemplatesForLabels shortcut: take the union of every
	// stream's label set (any one label on any one stream matches). The
	// engine's TemplatesForLabels path is AND-only, so walk stream by stream
	// and union the resulting template IDs instead.
	seen := make(map[string]*logwire.LogTemplate)
	for _, s := range streams {
		for k, v := range s.Labels {
			for _, t := range engine.TemplatesForLabels(map[string]string{k: v}) {
				if t == nil {
					continue
				}
				seen[t.ID] = t
			}
		}
	}
	out := make([]*logwire.LogTemplate, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// formatLogsDrillDown renders the drill-down response: a summary line,
// the matching streams (capped for readability), and the templates they
// reference. Each stream and template line is rendered as
// `<alias> (<8-char-hash>)` so operators can either copy the readable
// alias or, when the alias is empty (defensive — should not happen
// post-Phase-2), still locate the object by its short hash.
func formatLogsDrillDown(
	queryID, text string,
	labelFilters map[string]string,
	minSev string,
	streams []*logwire.LogStream,
	templates []*logwire.LogTemplate,
	engine *logs.QueryEngine,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log drill-down — %s\n\n", queryID)
	fmt.Fprintf(&sb, "**Filter:** `%s`\n\n", text)
	if len(labelFilters) > 0 {
		keys := make([]string, 0, len(labelFilters))
		for k := range labelFilters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, labelFilters[k]))
		}
		fmt.Fprintf(&sb, "- labels: %s\n", strings.Join(parts, ", "))
	}
	if minSev != "" {
		fmt.Fprintf(&sb, "- severity >= %s\n", minSev)
	}
	fmt.Fprintf(&sb, "\n### Streams (%d)\n\n", len(streams))
	const streamCap = 25
	for i, s := range streams {
		if i >= streamCap {
			fmt.Fprintf(&sb, "...and %d more\n", len(streams)-streamCap)
			break
		}
		fmt.Fprintf(&sb, "- %s %s\n", renderStreamLocator(engine, s), formatStreamLabels(s))
	}
	fmt.Fprintf(&sb, "\n### Templates (%d)\n\n", len(templates))
	const tplCap = 25
	for i, t := range templates {
		if i >= tplCap {
			fmt.Fprintf(&sb, "...and %d more\n", len(templates)-tplCap)
			break
		}
		fmt.Fprintf(&sb, "- %s [%s] %s\n", renderTemplateLocator(engine, t), t.Severity, t.Pattern)
	}
	return sb.String()
}

// renderStreamLocator formats the alias + short-hash pair used in every
// drill-down list.
func renderStreamLocator(engine *logs.QueryEngine, s *logwire.LogStream) string {
	short := s.ID
	if len(short) > 8 {
		short = short[:8]
	}
	if engine == nil {
		return fmt.Sprintf("`%s`", s.ID)
	}
	return fmt.Sprintf("`%s` (%s)", engine.AliasForStreamID(s.ID), short)
}

// renderTemplateLocator is the template counterpart to
// renderStreamLocator.
func renderTemplateLocator(engine *logs.QueryEngine, t *logwire.LogTemplate) string {
	short := t.ID
	if len(short) > 8 {
		short = short[:8]
	}
	if engine == nil {
		return fmt.Sprintf("`%s`", t.ID)
	}
	return fmt.Sprintf("`%s` (%s)", engine.AliasForTemplateID(t.ID), short)
}

// formatStreamLabels renders a stream's label set as a compact "k=v, k=v"
// string, sorted alphabetically so tests aren't at the mercy of map order.
func formatStreamLabels(s *logwire.LogStream) string {
	if len(s.Labels) == 0 {
		return "(no labels)"
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, s.Labels[k]))
	}
	return strings.Join(parts, ", ")
}

// handleLogsTemplateDetail and its helpers live in tools_logs_query_template.go
// to keep both files under the 300-line soft cap.
