// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph timeline mode.
//
// `query({ graph: "logs", name: "<id>", mode: "timeline" })` returns
// every indexed template ordered by FirstSeen ascending, relative to
// the earliest observed event (T+0s). This recovers the temporal
// ordering the overview mode throws away. Passing `bucket="<Go
// duration>"` (e.g. "10s", "1m") switches to a histogram-style rollup
// grouping templates into fixed-width windows.
//
// Templates without timestamps (rare — legacy graphs without pipeline
// timestamps) are listed in a trailing section so they aren't silently
// dropped.
package tools

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// timelineRow is a single ranked template entry in the linear
// timeline view. Offset is measured from T0, the earliest FirstSeen
// across all templates.
type timelineRow struct {
	Template *logwire.LogTemplate
	Offset   time.Duration // FirstSeen − T0
	Span     time.Duration // LastSeen − FirstSeen
}

// handleLogsTimeline is the graph='logs' mode='timeline' entry point.
// When bucket is empty the view is a flat list ordered by FirstSeen;
// when bucket parses as a Go duration the view switches to a
// histogram-style rollup with one row per bucket.
func handleLogsTimeline(queryID string, engine *logs.QueryEngine, bucket string) kgtools.ToolResult {
	timed, untimed := partitionTemplatesByTiming(engine.Templates())
	if len(timed) == 0 {
		return kgtools.TextResult(noTimedTemplatesMessage(queryID, untimed))
	}
	sortTemplatesByFirstSeen(timed)
	t0 := timed[0].FirstSeen
	rows := buildTimelineRows(timed, t0)

	if strings.TrimSpace(bucket) != "" {
		dur, err := time.ParseDuration(bucket)
		if err != nil {
			return kgtools.ErrorResult(fmt.Sprintf(
				"logs timeline %q: invalid bucket %q: %s (expected Go duration like '10s' or '1m')",
				queryID, bucket, err.Error()))
		}
		if dur <= 0 {
			return kgtools.ErrorResult(fmt.Sprintf(
				"logs timeline %q: bucket must be positive, got %q", queryID, bucket))
		}
		return kgtools.TextResult(formatTimelineBucketed(queryID, rows, t0, dur, untimed))
	}
	return kgtools.TextResult(formatTimelineFlat(queryID, rows, t0, untimed))
}

// partitionTemplatesByTiming splits templates into those with usable
// FirstSeen and those without. The untimed bucket is carried through
// rendering so callers can see what data would have been included
// under a richer pipeline.
func partitionTemplatesByTiming(all []*logwire.LogTemplate) (timed, untimed []*logwire.LogTemplate) {
	for _, t := range all {
		if t == nil {
			continue
		}
		if t.FirstSeen.IsZero() {
			untimed = append(untimed, t)
			continue
		}
		timed = append(timed, t)
	}
	return timed, untimed
}

// sortTemplatesByFirstSeen orders templates ascending by FirstSeen
// with alias/pattern tie-break so repeated runs produce stable output.
func sortTemplatesByFirstSeen(timed []*logwire.LogTemplate) {
	sort.Slice(timed, func(i, j int) bool {
		if !timed[i].FirstSeen.Equal(timed[j].FirstSeen) {
			return timed[i].FirstSeen.Before(timed[j].FirstSeen)
		}
		return templateSortKey(timed[i]) < templateSortKey(timed[j])
	})
}

// templateSortKey returns the alias when set, else the pattern, else
// the ID — used only for tie-breaks within the same FirstSeen.
func templateSortKey(t *logwire.LogTemplate) string {
	if t == nil {
		return ""
	}
	if t.Alias != "" {
		return t.Alias
	}
	if t.Pattern != "" {
		return t.Pattern
	}
	return t.ID
}

// buildTimelineRows constructs the ordered slice of timelineRow entries
// relative to T0.
func buildTimelineRows(timed []*logwire.LogTemplate, t0 time.Time) []timelineRow {
	rows := make([]timelineRow, 0, len(timed))
	for _, t := range timed {
		span := time.Duration(0)
		if !t.LastSeen.IsZero() && t.LastSeen.After(t.FirstSeen) {
			span = t.LastSeen.Sub(t.FirstSeen)
		}
		rows = append(rows, timelineRow{
			Template: t,
			Offset:   t.FirstSeen.Sub(t0),
			Span:     span,
		})
	}
	return rows
}

// formatTimelineFlat renders the linear (non-bucketed) timeline as a
// markdown table.
func formatTimelineFlat(
	queryID string,
	rows []timelineRow,
	t0 time.Time,
	untimed []*logwire.LogTemplate,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log timeline — %s\n\n", queryID)
	tEnd := rows[len(rows)-1].Template.LastSeen
	if tEnd.Before(rows[len(rows)-1].Template.FirstSeen) {
		tEnd = rows[len(rows)-1].Template.FirstSeen
	}
	fmt.Fprintf(&sb,
		"**T0:** %s · **span:** %s · **%d timed template(s)**\n\n",
		t0.Format("2006-01-02T15:04:05Z07:00"),
		tEnd.Sub(t0).Truncate(time.Second),
		len(rows))
	sb.WriteString("| offset | template | severity | count | span |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "| T+%s | %s | %s | %d | %s |\n",
			renderOffset(r.Offset),
			renderTimelineTemplate(r.Template),
			severityShort(r.Template.Severity),
			r.Template.Count,
			renderSpan(r.Span))
	}
	appendUntimedSection(&sb, untimed)
	return sb.String()
}

// formatTimelineBucketed renders the histogram view: one row per
// bucket window with the templates that fired in it. Bucket start is
// aligned to T0 (T+0s, T+bucket, T+2×bucket, ...). Empty buckets are
// dropped so the output stays compact.
func formatTimelineBucketed(
	queryID string,
	rows []timelineRow,
	t0 time.Time,
	bucket time.Duration,
	untimed []*logwire.LogTemplate,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log timeline (bucketed) — %s\n\n", queryID)
	fmt.Fprintf(&sb, "**T0:** %s · **bucket:** %s · **%d timed template(s)**\n\n",
		t0.Format("2006-01-02T15:04:05Z07:00"),
		bucket.Truncate(time.Second), len(rows))
	buckets := groupRowsByBucket(rows, bucket)
	sb.WriteString("| window | templates | total count |\n")
	sb.WriteString("|---|---|---|\n")
	for _, b := range buckets {
		fmt.Fprintf(&sb, "| T+%s–T+%s | %s | %d |\n",
			renderOffset(b.offset),
			renderOffset(b.offset+bucket),
			renderBucketTemplates(b.rows),
			bucketTotalCount(b.rows))
	}
	appendUntimedSection(&sb, untimed)
	return sb.String()
}

// bucketGroup aggregates timeline rows that share the same bucket
// offset (floor(row.Offset / bucket) × bucket).
type bucketGroup struct {
	offset time.Duration
	rows   []timelineRow
}

// groupRowsByBucket buckets rows by their offset and returns groups in
// ascending order. Empty buckets are skipped.
func groupRowsByBucket(rows []timelineRow, bucket time.Duration) []bucketGroup {
	if bucket <= 0 {
		return nil
	}
	byOffset := make(map[time.Duration]*bucketGroup)
	var keys []time.Duration
	for _, r := range rows {
		start := (r.Offset / bucket) * bucket
		g, ok := byOffset[start]
		if !ok {
			g = &bucketGroup{offset: start}
			byOffset[start] = g
			keys = append(keys, start)
		}
		g.rows = append(g.rows, r)
	}
	slices.Sort(keys)
	out := make([]bucketGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byOffset[k])
	}
	return out
}

// renderBucketTemplates joins the templates in a bucket as
// "alias ×count, alias ×count" with count suppressed when 1.
func renderBucketTemplates(rows []timelineRow) string {
	if len(rows) == 0 {
		return "_empty_"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		alias := templateSortKey(r.Template)
		if r.Template.Count > 1 {
			parts = append(parts, fmt.Sprintf("`%s` ×%d", alias, r.Template.Count))
			continue
		}
		parts = append(parts, fmt.Sprintf("`%s`", alias))
	}
	return strings.Join(parts, ", ")
}

// bucketTotalCount sums the Count field across a bucket's rows so the
// histogram column reflects entries, not template count.
func bucketTotalCount(rows []timelineRow) int {
	n := 0
	for _, r := range rows {
		n += r.Template.Count
	}
	return n
}

// renderTimelineTemplate renders a template for a row: "`alias` —
// pattern" with the pattern truncated so the row stays readable.
func renderTimelineTemplate(t *logwire.LogTemplate) string {
	if t == nil {
		return "_unknown_"
	}
	alias := t.Alias
	if alias == "" {
		alias = shortHex(t.ID)
	}
	pattern := t.Pattern
	if len(pattern) > 55 {
		pattern = pattern[:55] + "…"
	}
	if pattern == "" {
		return fmt.Sprintf("`%s`", alias)
	}
	return fmt.Sprintf("`%s` — %s", alias, pattern)
}

// renderOffset formats a duration as a compact "Ns", "Nm", or "NmNs"
// relative-time string for the T+ prefix.
func renderOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

// renderSpan formats a template's span. Zero spans render as "0s" for
// visual consistency rather than blank.
func renderSpan(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Truncate(time.Second).String()
}

// severityShort maps canonical severities to a 3-4 char tag for the
// timeline severity column. Unknown severities pass through.
func severityShort(sev string) string {
	switch strings.ToUpper(sev) {
	case logwire.SeverityCritical:
		return "CRIT"
	case logwire.SeverityError:
		return "ERR"
	case logwire.SeverityWarn:
		return "WARN"
	case logwire.SeverityInfo:
		return "INFO"
	case logwire.SeverityDebug:
		return "DBG"
	case logwire.SeverityTrace:
		return "TRC"
	default:
		return sev
	}
}

// appendUntimedSection adds a trailing section listing any templates
// that lacked FirstSeen — keeps them visible without distorting the
// ordered output.
func appendUntimedSection(sb *strings.Builder, untimed []*logwire.LogTemplate) {
	if len(untimed) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n**Untimed templates (%d)** — no FirstSeen recorded:\n", len(untimed))
	for _, t := range untimed {
		fmt.Fprintf(sb, "- %s\n", renderTimelineTemplate(t))
	}
}

// noTimedTemplatesMessage returns the degraded-but-useful output when
// no template carries a FirstSeen.
func noTimedTemplatesMessage(queryID string, untimed []*logwire.LogTemplate) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log timeline — %s\n\n", queryID)
	sb.WriteString("_No templates carry FirstSeen timestamps — timeline ordering unavailable._\n")
	appendUntimedSection(&sb, untimed)
	return sb.String()
}
