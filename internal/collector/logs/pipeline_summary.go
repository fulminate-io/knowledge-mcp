// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"fmt"
	"sort"
	"strings"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// summaryMaxPatterns caps how many error/warning templates we list in
// each section of the summary. The LLM consumer only needs the top
// offenders; a verbose list crowds out the actionable signal.
const (
	summaryMaxErrorPatterns   = 10
	summaryMaxWarningPatterns = 5
	summaryMaxServices        = 8
	summaryMaxChars           = 2000
)

// buildSummary renders the collected artifacts into a structured text
// block suitable for LLM context. The structure is:
//
//	Header — time range, totals
//	Top error templates
//	Top warning templates
//	Affected services (ranked by error count)
//	Correlations (confirmed + possibly-related)
//
// Summary length is soft-capped at summaryMaxChars; content above the
// cap is truncated with a "…" marker so callers can count on a bounded
// string even for pathologically wide collections.
func buildSummary(
	templates []*wirelogs.LogTemplate,
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	correlations []wirelogs.CorrelationResult,
	agg *AggregationSummary,
	query wirelogs.Query,
) string {
	var b strings.Builder

	writeSummaryHeader(&b, templates, streams, chunks, query)
	writeConcentrationSection(&b, agg, totalEntryCount(chunks))
	writeTopPatterns(&b, templates)
	writeAffectedServices(&b, agg)
	writeCorrelationsSection(&b, correlations, templates)

	out := b.String()
	if len(out) > summaryMaxChars {
		out = out[:summaryMaxChars-1] + "…"
	}
	return out
}

// writeSummaryHeader writes the header section with time range and
// aggregate counts.
func writeSummaryHeader(
	b *strings.Builder,
	templates []*wirelogs.LogTemplate,
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	query wirelogs.Query,
) {
	start, end := collectTimeRange(query, chunks)
	fmt.Fprintf(b, "Log Collection Summary\n")
	fmt.Fprintf(b, "Time range: %s to %s\n", formatTime(start), formatTime(end))
	fmt.Fprintf(b, "Total entries: %d across %d streams and %d templates\n\n",
		totalEntryCount(chunks), len(streams), len(templates))
}

// totalEntryCount sums EntryCount across every chunk. Hoisted out of
// writeSummaryHeader so the concentration section can reuse it without
// re-walking the slice.
func totalEntryCount(chunks []*wirelogs.LogChunk) int {
	n := 0
	for _, c := range chunks {
		n += c.EntryCount
	}
	return n
}

// collectTimeRange prefers the query's explicit range; when empty it
// falls back to the min/max chunk timestamps observed.
func collectTimeRange(query wirelogs.Query, chunks []*wirelogs.LogChunk) (time.Time, time.Time) {
	if !query.StartTime.IsZero() && !query.EndTime.IsZero() {
		return query.StartTime, query.EndTime
	}
	var start, end time.Time
	for _, c := range chunks {
		if start.IsZero() || c.StartTime.Before(start) {
			start = c.StartTime
		}
		if c.EndTime.After(end) {
			end = c.EndTime
		}
	}
	return start, end
}

// writeTopPatterns lists the highest-count error and warning templates.
// Each line is terse to preserve LLM-context budget.
func writeTopPatterns(b *strings.Builder, templates []*wirelogs.LogTemplate) {
	errors := templatesAtOrAbove(templates, wirelogs.SeverityError)
	warnings := templatesAtLevel(templates, wirelogs.SeverityWarn)
	sortByCountDesc(errors)
	sortByCountDesc(warnings)

	if len(errors) > 0 {
		fmt.Fprintf(b, "Top Error Patterns (top %d):\n", min(len(errors), summaryMaxErrorPatterns))
		writePatternList(b, errors, summaryMaxErrorPatterns)
		b.WriteString("\n")
	}
	if len(warnings) > 0 {
		fmt.Fprintf(b, "Top Warning Patterns (top %d):\n", min(len(warnings), summaryMaxWarningPatterns))
		writePatternList(b, warnings, summaryMaxWarningPatterns)
		b.WriteString("\n")
	}
}

// writePatternList renders up to limit templates as
// "  - <alias> count× Pattern". The alias prefix lets summary readers
// reference templates by their readable form (e.g. `node-not-ready@warn`)
// instead of the raw SHA-256 hash. Falls back to a hex-only locator when
// no alias is present.
func writePatternList(b *strings.Builder, list []*wirelogs.LogTemplate, limit int) {
	n := min(len(list), limit)
	for i := range n {
		t := list[i]
		fmt.Fprintf(b, "  - `%s` %d× %s [%s–%s]\n",
			templateLocator(t),
			t.Count,
			truncatePattern(t.Pattern, 120),
			formatTimeShort(t.FirstSeen),
			formatTimeShort(t.LastSeen),
		)
	}
}

// templateLocator + correlationLocator + buildTemplateLookup live in
// pipeline_summary_locator.go to keep this file under the 300-line cap.

// writeAffectedServices ranks services by error count using the
// AggregationSummary. Skipped when agg is nil or lacks a "service" key.
func writeAffectedServices(b *strings.Builder, agg *AggregationSummary) {
	if agg == nil {
		return
	}
	ranked := agg.TopK(wirelogs.FieldService, summaryMaxServices, "error_count")
	if len(ranked) == 0 {
		// fallback: try namespace.
		ranked = agg.TopK(wirelogs.FieldNamespace, summaryMaxServices, "error_count")
	}
	if len(ranked) == 0 {
		return
	}
	b.WriteString("Affected Services (by error count):\n")
	for _, r := range ranked {
		if r.Stats == nil {
			continue
		}
		fmt.Fprintf(b, "  - %s: %d entries (%d error, %d warn, %d info)\n",
			r.Value,
			r.Stats.TotalCount,
			r.Stats.ErrorCount,
			r.Stats.WarnCount,
			r.Stats.InfoCount,
		)
	}
	b.WriteString("\n")
}

// writeCorrelationsSection renders confirmed correlations as graph edges
// and unconfirmed ones as "possibly related" hints. Distinct from
// writeCorrelations in pipeline_correlation.go, which emits edges into
// the log graph. The templates list is consulted to surface aliases for
// each correlated template ID.
func writeCorrelationsSection(
	b *strings.Builder,
	correlations []wirelogs.CorrelationResult,
	templates []*wirelogs.LogTemplate,
) {
	if len(correlations) == 0 {
		return
	}
	var confirmed, possible []wirelogs.CorrelationResult
	for _, c := range correlations {
		if c.StructurallyConfirmed {
			confirmed = append(confirmed, c)
		} else {
			possible = append(possible, c)
		}
	}
	templateLookup := buildTemplateLookup(templates)
	if len(confirmed) > 0 {
		b.WriteString("Correlations Found (confirmed):\n")
		for _, c := range confirmed {
			fmt.Fprintf(b, "  - %s ↔ %s (services %s↔%s, score %.2f)\n",
				correlationLocator(c.TemplateA, templateLookup),
				correlationLocator(c.TemplateB, templateLookup),
				c.ServiceA, c.ServiceB, c.CooccurrenceScore)
		}
		b.WriteString("\n")
	}
	if len(possible) > 0 {
		b.WriteString("Possibly Related (temporal overlap, no cloud dependency — may be coincidence):\n")
		for _, c := range possible {
			fmt.Fprintf(b, "  - %s on %s ↔ %s on %s (score %.2f)\n",
				correlationLocator(c.TemplateA, templateLookup), c.ServiceA,
				correlationLocator(c.TemplateB, templateLookup), c.ServiceB,
				c.CooccurrenceScore)
		}
		b.WriteString("\n")
	}
}

// templatesAtOrAbove returns templates with Severity ≥ minSeverity.
func templatesAtOrAbove(templates []*wirelogs.LogTemplate, minSeverity string) []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, 0, len(templates))
	for _, t := range templates {
		if t == nil {
			continue
		}
		if wirelogs.SeverityAtLeast(t.Severity, minSeverity) {
			out = append(out, t)
		}
	}
	return out
}

// templatesAtLevel returns templates whose Severity matches exactly.
// Used for the warnings section (we don't want to re-list errors).
func templatesAtLevel(templates []*wirelogs.LogTemplate, level string) []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, 0, len(templates))
	for _, t := range templates {
		if t == nil {
			continue
		}
		if t.Severity == level {
			out = append(out, t)
		}
	}
	return out
}

// sortByCountDesc sorts templates by Count descending, breaking ties on
// ID for deterministic output.
func sortByCountDesc(list []*wirelogs.LogTemplate) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].ID < list[j].ID
	})
}

// truncatePattern trims long patterns to keep summary lines readable.
func truncatePattern(p string, max int) string {
	if len(p) <= max {
		return p
	}
	return p[:max-1] + "…"
}

// formatTime renders a timestamp in a compact UTC format.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// formatTimeShort renders HH:MM:SS — used in pattern lists to keep each
// line under a readable width.
func formatTimeShort(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.UTC().Format("15:04:05")
}

// shortID returns the last 8 chars of an ID — enough to disambiguate in
// a summary line without consuming the full SHA-256 hash width.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}
