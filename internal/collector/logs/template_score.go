// SPDX-License-Identifier: Apache-2.0

// Package logs — template signal-strength scoring.
//
// ScoreTemplate combines count, correlation count, severity, and
// recency into a single ranking score. Used by the query overview /
// drill-down render paths to surface the most operator-relevant
// templates first instead of alphabetical / ingestion order.
package logs

import (
	"math"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// SeverityWeight returns the per-severity multiplier used by
// ScoreTemplate. Higher = more important to triage. Unknown
// severities pass through with weight 1.0 so non-standard providers
// don't lose ranking entirely.
func SeverityWeight(sev string) float64 {
	switch sev {
	case wirelogs.SeverityCritical:
		return 5.0
	case wirelogs.SeverityError:
		return 3.0
	case wirelogs.SeverityWarn:
		return 1.5
	case wirelogs.SeverityInfo:
		return 1.0
	case wirelogs.SeverityDebug:
		return 0.5
	case wirelogs.SeverityTrace:
		return 0.25
	default:
		return 1.0
	}
}

// ScoreTemplate produces a single floating-point score:
//
//	count × (1 + log1p(correlations)) × severity_weight × recency_decay
//
// Templates with no entries score 0 (nothing to rank). Otherwise:
//   - count is the raw entry count (the dominant signal).
//   - correlation boost: 1 + log(1 + N). Zero correlations → ×1, ten
//     correlations → ×3.4. Diminishing returns prevent a small number
//     of correlations from dominating the score.
//   - severity_weight: 5 for CRITICAL down to 0.25 for TRACE.
//   - recency_decay: 0.5..1.0 relative to the graph's time window.
//     Recent templates earn the full multiplier; older ones still
//     count, but at half weight.
func ScoreTemplate(t *wirelogs.LogTemplate, corrCount int, graphFirst, graphLast time.Time) float64 {
	if t == nil {
		return 0
	}
	count := float64(t.Count)
	if count <= 0 {
		return 0
	}
	corrBoost := 1 + math.Log1p(float64(corrCount))
	sev := SeverityWeight(t.Severity)
	rec := recencyDecay(t.LastSeen, graphFirst, graphLast)
	return count * corrBoost * sev * rec
}

// recencyDecay maps a template's LastSeen to a 0.5..1.0 multiplier
// relative to the graph's time window. The most-recent template gets
// 1.0; the oldest gets 0.5. Returns 1.0 when timestamps are missing
// or the graph window is degenerate (collapses to "no recency
// adjustment").
func recencyDecay(tLast, graphFirst, graphLast time.Time) float64 {
	if tLast.IsZero() || graphFirst.IsZero() || graphLast.IsZero() {
		return 1.0
	}
	span := graphLast.Sub(graphFirst)
	if span <= 0 {
		return 1.0
	}
	rel := float64(tLast.Sub(graphFirst)) / float64(span)
	switch {
	case rel < 0:
		return 0.5
	case rel > 1:
		return 1.0
	default:
		return 0.5 + 0.5*rel
	}
}
