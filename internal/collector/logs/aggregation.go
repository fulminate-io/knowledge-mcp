// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"slices"
	"sort"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// LabelValueStats holds pre-computed stats for a single label key-value pair.
type LabelValueStats struct {
	TotalCount  int
	ErrorCount  int
	WarnCount   int
	InfoCount   int
	TemplateIDs []string
	FirstSeen   time.Time
	LastSeen    time.Time
}

// LabelValueRanked pairs a label value with its pre-computed stats.
type LabelValueRanked struct {
	Value string
	Stats *LabelValueStats
}

// AggregationSummary maps labelKey -> labelValue -> stats, providing
// pre-computed per-label-value statistics for overview/triage queries.
type AggregationSummary struct {
	stats map[string]map[string]*LabelValueStats
}

// BuildAggregationSummary constructs an AggregationSummary from streams,
// chunks, and a template lookup map. For each stream it finds all chunks,
// classifies entry counts by the linked template's severity, and
// accumulates stats into every label key-value pair the stream carries.
func BuildAggregationSummary(
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	templates map[string]*wirelogs.LogTemplate,
) *AggregationSummary {
	agg := &AggregationSummary{
		stats: make(map[string]map[string]*LabelValueStats),
	}

	// Index chunks by stream ID for O(1) lookup.
	chunksByStream := indexChunksByStream(chunks)

	for _, stream := range streams {
		streamChunks := chunksByStream[stream.ID]
		for _, chunk := range streamChunks {
			tmpl := templates[chunk.TemplateID]
			if tmpl == nil {
				continue
			}
			accumulateChunk(agg, stream.Labels, chunk, tmpl)
		}
	}

	return agg
}

// indexChunksByStream groups chunks by their StreamID.
func indexChunksByStream(chunks []*wirelogs.LogChunk) map[string][]*wirelogs.LogChunk {
	m := make(map[string][]*wirelogs.LogChunk, len(chunks))
	for _, c := range chunks {
		m[c.StreamID] = append(m[c.StreamID], c)
	}
	return m
}

// accumulateChunk adds a single chunk's counts into every label key-value
// pair from the given label set.
func accumulateChunk(
	agg *AggregationSummary,
	labels map[string]string,
	chunk *wirelogs.LogChunk,
	tmpl *wirelogs.LogTemplate,
) {
	for key, value := range labels {
		s := agg.getOrCreate(key, value)
		s.TotalCount += chunk.EntryCount
		addSeverityCounts(s, tmpl.Severity, chunk.EntryCount)
		addTemplateID(s, chunk.TemplateID)
		extendTimeRange(s, chunk.StartTime, chunk.EndTime)
	}
}

// getOrCreate returns the stats entry for a label key-value pair,
// creating it if it does not exist.
func (a *AggregationSummary) getOrCreate(key, value string) *LabelValueStats {
	byValue, ok := a.stats[key]
	if !ok {
		byValue = make(map[string]*LabelValueStats)
		a.stats[key] = byValue
	}
	s, ok := byValue[value]
	if !ok {
		s = &LabelValueStats{}
		byValue[value] = s
	}
	return s
}

// addSeverityCounts classifies entryCount into exclusive severity buckets.
func addSeverityCounts(s *LabelValueStats, severity string, count int) {
	switch {
	case wirelogs.SeverityAtLeast(severity, wirelogs.SeverityError):
		s.ErrorCount += count
	case wirelogs.SeverityAtLeast(severity, wirelogs.SeverityWarn):
		s.WarnCount += count
	default:
		s.InfoCount += count
	}
}

// addTemplateID appends tmplID to s.TemplateIDs if not already present.
func addTemplateID(s *LabelValueStats, tmplID string) {
	if slices.Contains(s.TemplateIDs, tmplID) {
		return
	}
	s.TemplateIDs = append(s.TemplateIDs, tmplID)
}

// extendTimeRange extends FirstSeen/LastSeen to cover the chunk boundaries.
func extendTimeRange(s *LabelValueStats, start, end time.Time) {
	if s.FirstSeen.IsZero() || start.Before(s.FirstSeen) {
		s.FirstSeen = start
	}
	if end.After(s.LastSeen) {
		s.LastSeen = end
	}
}

// StatsFor returns the pre-computed stats for a specific label key-value
// pair, or nil if the pair was not seen.
func (a *AggregationSummary) StatsFor(key, value string) *LabelValueStats {
	if byValue, ok := a.stats[key]; ok {
		return byValue[value]
	}
	return nil
}

// TopK returns the top n values for a label key, sorted descending by
// sortBy ("error_count", "warn_count", or "total_count"). Defaults to
// "error_count" for unrecognized sortBy values. If n exceeds the number
// of values, all values are returned.
func (a *AggregationSummary) TopK(key string, n int, sortBy string) []LabelValueRanked {
	byValue, ok := a.stats[key]
	if !ok {
		return nil
	}
	ranked := make([]LabelValueRanked, 0, len(byValue))
	for v, s := range byValue {
		ranked = append(ranked, LabelValueRanked{Value: v, Stats: s})
	}
	sortFunc := sortKeyFunc(sortBy)
	sort.Slice(ranked, func(i, j int) bool {
		return sortFunc(ranked[i].Stats) > sortFunc(ranked[j].Stats)
	})
	if n > len(ranked) {
		n = len(ranked)
	}
	return ranked[:n]
}

// sortKeyFunc returns an accessor for the sort field.
func sortKeyFunc(sortBy string) func(*LabelValueStats) int {
	switch sortBy {
	case "warn_count":
		return func(s *LabelValueStats) int { return s.WarnCount }
	case "total_count":
		return func(s *LabelValueStats) int { return s.TotalCount }
	default: // "error_count"
		return func(s *LabelValueStats) int { return s.ErrorCount }
	}
}

// Keys returns all indexed label keys, sorted alphabetically.
func (a *AggregationSummary) Keys() []string {
	keys := make([]string, 0, len(a.stats))
	for k := range a.stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Values returns all values for a label key, sorted alphabetically.
func (a *AggregationSummary) Values(key string) []string {
	byValue, ok := a.stats[key]
	if !ok {
		return nil
	}
	vals := make([]string, 0, len(byValue))
	for v := range byValue {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	return vals
}
