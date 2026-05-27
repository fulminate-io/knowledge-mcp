// SPDX-License-Identifier: Apache-2.0

// Package logs — concentration heuristic for the collect summary.
//
// Computes a single observation: for each label dimension, what
// fraction of total entries falls on the top value. Dimensions where
// the top value claims ≥concentrationThreshold of the total are
// flagged as "concentrated on <key>=<value>" — the orientation the
// agent reads first when triaging a fresh collection.
package logs

import (
	"fmt"
	"sort"
	"strings"
)

// concentrationThreshold is the minimum top-value share required for
// a dimension to count as "concentrated." 50% matches the ticket
// spec — anything below that doesn't warrant a separate callout.
const concentrationThreshold = 0.5

// concentrationKeysSkipped lists label keys whose concentration is
// uninteresting to surface even when ≥50%. cluster_name and
// project_id are typically degenerate (one cluster per collection)
// so flagging them just adds noise.
var concentrationKeysSkipped = map[string]bool{
	"cluster_name": true,
	"project_id":   true,
}

// concentrationFinding is one flagged dimension. Share is the fraction
// (0..1) of total entries that fell on Value.
type concentrationFinding struct {
	Key   string
	Value string
	Count int
	Total int
	Share float64
}

// findConcentrations returns every label dimension whose top value
// claims ≥concentrationThreshold of totalEntries. Sorted by Share
// descending so the most striking signal comes first.
func findConcentrations(agg *AggregationSummary, totalEntries int) []concentrationFinding {
	if agg == nil || totalEntries <= 0 {
		return nil
	}
	var out []concentrationFinding
	for _, key := range agg.Keys() {
		if concentrationKeysSkipped[key] {
			continue
		}
		top := agg.TopK(key, 1, "total_count")
		if len(top) == 0 || top[0].Stats == nil {
			continue
		}
		share := float64(top[0].Stats.TotalCount) / float64(totalEntries)
		if share <= concentrationThreshold {
			continue
		}
		out = append(out, concentrationFinding{
			Key:   key,
			Value: top[0].Value,
			Count: top[0].Stats.TotalCount,
			Total: totalEntries,
			Share: share,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Share > out[j].Share })
	return out
}

// writeConcentrationSection appends a "Concentration" callout block
// to the collect summary. Silent when no dimension exceeds the
// threshold. Format kept terse — this is a header section the agent
// scans in <2 seconds before drilling deeper.
func writeConcentrationSection(b *strings.Builder, agg *AggregationSummary, totalEntries int) {
	findings := findConcentrations(agg, totalEntries)
	if len(findings) == 0 {
		return
	}
	b.WriteString("Concentration (events skewed onto a single label value):\n")
	for _, f := range findings {
		fmt.Fprintf(b, "  - %s=%s — %d of %d entries (%d%%)\n",
			f.Key, f.Value, f.Count, f.Total, int(f.Share*100+0.5))
	}
	b.WriteString("\n")
}
