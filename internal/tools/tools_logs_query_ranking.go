// SPDX-License-Identifier: Apache-2.0

// Package tools — template signal-strength ranking helpers used by
// log overview and drill-down render paths. Keeping the ranking
// machinery out of tools_logs_query_format.go lets that file stay
// under the 300-line soft cap.
package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// rankedTemplate pairs a template with its computed signal score and
// the correlation peer count that fed into the score (surfaced
// directly in the rendered table).
type rankedTemplate struct {
	Template  *logwire.LogTemplate
	Score     float64
	CorrCount int
}

// rankTemplatesBySignal computes a signal score per template and
// returns the rows sorted by score desc (alphabetical pattern
// tiebreak). Pass the full template set; the caller is responsible
// for any pre-filtering. The logDB argument may be nil — the function
// degrades to count×severity×recency without correlation boost.
func rankTemplatesBySignal(
	st *logState,
	templates []*logwire.LogTemplate,
) []rankedTemplate {
	if len(templates) == 0 {
		return nil
	}
	corrCounts := correlationCountsForTemplates(st, templates)
	graphFirst, graphLast := graphTimeSpan(templates)
	rows := make([]rankedTemplate, 0, len(templates))
	for _, t := range templates {
		if t == nil {
			continue
		}
		rows = append(rows, rankedTemplate{
			Template:  t,
			CorrCount: corrCounts[t.ID],
			Score:     logs.ScoreTemplate(t, corrCounts[t.ID], graphFirst, graphLast),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].Template.Pattern < rows[j].Template.Pattern
	})
	return rows
}

// correlationCountsForTemplates iterates both directions of
// CORRELATES_WITH edges per template and returns templateID →
// unique-peer count. Returns an empty map when logDB is nil.
func correlationCountsForTemplates(
	st *logState,
	templates []*logwire.LogTemplate,
) map[string]int {
	out := make(map[string]int, len(templates))
	if st == nil {
		return out
	}
	for _, t := range templates {
		if t == nil {
			continue
		}
		out[t.ID] = countTemplateCorrelationPeers(st, t.ID)
	}
	return out
}

// countTemplateCorrelationPeers returns the number of distinct peer
// templates this one correlates with, walking both edge directions.
// Extracted so the per-template cost is one function call (and the
// gocognit budget for the parent is preserved).
func countTemplateCorrelationPeers(st *logState, tmplID string) int {
	seen := make(map[string]struct{})
	for _, dir := range []kgwire.EdgeDirection{kgwire.OutgoingEdges, kgwire.IncomingEdges} {
		edges := st.EdgesOf(tmplID, dir, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
		for i := range edges {
			e := &edges[i]
			peer := e.ToId
			if dir == kgwire.IncomingEdges {
				peer = e.FromId
			}
			seen[peer] = struct{}{}
		}
	}
	return len(seen)
}

// graphTimeSpan returns the (earliest FirstSeen, latest LastSeen)
// across a template set. Used to normalize recency. Either bound can
// be zero when no template carries timestamps — recencyDecay handles
// that case by returning 1.0.
func graphTimeSpan(templates []*logwire.LogTemplate) (time.Time, time.Time) {
	var first, last time.Time
	for _, t := range templates {
		if t == nil {
			continue
		}
		if !t.FirstSeen.IsZero() && (first.IsZero() || t.FirstSeen.Before(first)) {
			first = t.FirstSeen
		}
		if !t.LastSeen.IsZero() && (last.IsZero() || t.LastSeen.After(last)) {
			last = t.LastSeen
		}
	}
	return first, last
}

// writeTopTemplatesSection appends a "Top templates by signal"
// section to sb, capped at topN rows. Silent when ranked is empty.
// Emitted into the overview view so an agent reads "what's the most
// important pattern" from the first table.
func writeTopTemplatesSection(sb *strings.Builder, ranked []rankedTemplate, topN int) {
	if len(ranked) == 0 {
		return
	}
	if topN > len(ranked) {
		topN = len(ranked)
	}
	fmt.Fprintf(sb, "### Top templates by signal (%d of %d)\n\n", topN, len(ranked))
	sb.WriteString(
		"Score = count × (1 + log(1 + correlations)) × severity_weight × recency_decay.\n\n")
	sb.WriteString("| score | template | count | corr | severity | last seen |\n")
	sb.WriteString("|---|---|---|---|---|---|\n")
	for i := range topN {
		writeRankedTemplateRow(sb, ranked[i])
	}
	sb.WriteString("\n")
}

// writeRankedTemplateRow renders a single ranked template line.
// Extracted so writeTopTemplatesSection stays under the funlen cap.
func writeRankedTemplateRow(sb *strings.Builder, r rankedTemplate) {
	alias := r.Template.Alias
	if alias == "" {
		alias = shortHex(r.Template.ID)
	}
	last := "n/a"
	if !r.Template.LastSeen.IsZero() {
		last = r.Template.LastSeen.Format("15:04:05")
	}
	fmt.Fprintf(sb, "| %.1f | `%s` | %d | %d | %s | %s |\n",
		r.Score, alias, r.Template.Count, r.CorrCount,
		severityShort(r.Template.Severity), last)
}

// sortTemplatesBySignal sorts a template slice in-place by the same
// signal score as the overview. Used by drill-down to replace
// alphabetical pattern ordering.
func sortTemplatesBySignal(
	st *logState,
	templates []*logwire.LogTemplate,
) {
	if len(templates) <= 1 {
		return
	}
	corrCounts := correlationCountsForTemplates(st, templates)
	graphFirst, graphLast := graphTimeSpan(templates)
	sort.Slice(templates, func(i, j int) bool {
		si := logs.ScoreTemplate(templates[i], corrCounts[templates[i].ID], graphFirst, graphLast)
		sj := logs.ScoreTemplate(templates[j], corrCounts[templates[j].ID], graphFirst, graphLast)
		if si != sj {
			return si > sj
		}
		return templates[i].Pattern < templates[j].Pattern
	})
}
