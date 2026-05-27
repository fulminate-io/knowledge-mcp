// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// QueryEngine combines a LabelIndex, AggregationSummary, and coarse time
// filtering into a unified query interface for log exploration. It is the
// primary entry point for triage queries: "what error patterns exist for
// service=api?", "which streams have severity >= WARN?", etc.
type QueryEngine struct {
	labelIndex  *LabelIndex
	aggregation *AggregationSummary

	// streamByID maps hex stream ID -> *wirelogs.LogStream for pointer resolution.
	streamByID map[string]*wirelogs.LogStream

	// chunksByStream maps streamID -> chunks for time range filtering.
	chunksByStream map[string][]*wirelogs.LogChunk

	// templateByID maps templateID -> *wirelogs.LogTemplate for template resolution.
	templateByID map[string]*wirelogs.LogTemplate

	// streamByAlias maps lowercased alias -> hex stream ID. Aliases are
	// stored case-insensitive so callers can type either form. Collisions
	// are resolved at construction time by appending an 8-char hash
	// suffix; see assignStreamAliases.
	streamByAlias map[string]string

	// aliasByStreamID maps hex stream ID -> display-cased alias. The
	// reverse direction of streamByAlias, preserving original case for
	// rendering.
	aliasByStreamID map[string]string

	// templateByAlias maps lowercased alias -> hex template ID. Same
	// shape as streamByAlias; collisions get a 4-hex-char suffix because
	// templates are fewer and the severity suffix is already present.
	templateByAlias map[string]string

	// aliasByTemplateID maps hex template ID -> display-cased alias.
	aliasByTemplateID map[string]string
}

// NewQueryEngine builds a QueryEngine from raw streams, chunks, and templates.
// It constructs StreamIDMap, LabelIndex, and AggregationSummary internally.
func NewQueryEngine(
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	templates []*wirelogs.LogTemplate,
) *QueryEngine {
	idMap := NewStreamIDMap()
	for _, s := range streams {
		idMap.Add(s.ID)
	}

	li := NewLabelIndex(streams, chunks, templates, idMap)

	tmplMap := make(map[string]*wirelogs.LogTemplate, len(templates))
	for _, t := range templates {
		tmplMap[t.ID] = t
	}
	agg := BuildAggregationSummary(streams, chunks, tmplMap)

	streamByID := make(map[string]*wirelogs.LogStream, len(streams))
	for _, s := range streams {
		streamByID[s.ID] = s
	}

	chunksByStream := make(map[string][]*wirelogs.LogChunk, len(streams))
	for _, c := range chunks {
		chunksByStream[c.StreamID] = append(chunksByStream[c.StreamID], c)
	}

	qe := &QueryEngine{
		labelIndex:     li,
		aggregation:    agg,
		streamByID:     streamByID,
		chunksByStream: chunksByStream,
		templateByID:   tmplMap,
	}
	qe.streamByAlias, qe.aliasByStreamID = assignStreamAliases(streams)
	qe.templateByAlias, qe.aliasByTemplateID = assignTemplateAliases(templates)
	return qe
}

// Overview returns the top-ranked label values for every indexed key,
// sorted by error_count descending. This is the triage overview query.
func (qe *QueryEngine) Overview() map[string][]LabelValueRanked {
	keys := qe.aggregation.Keys()
	result := make(map[string][]LabelValueRanked, len(keys))
	for _, key := range keys {
		ranked := qe.aggregation.TopK(key, topKDefault, "error_count")
		if ranked != nil {
			result[key] = ranked
		}
	}
	return result
}

// topKDefault is the default number of values returned per key in Overview.
const topKDefault = 100

// QueryLabels returns streams matching the AND intersection of all label
// filters. Returns nil if no streams match.
func (qe *QueryEngine) QueryLabels(filters map[string]string) []*wirelogs.LogStream {
	bm := qe.labelIndex.MatchLabels(filters)
	ids := qe.labelIndex.ResolveStreamIDs(bm)
	return qe.resolveStreams(ids)
}

// QuerySeverityRange returns all streams with max severity at or above
// minSeverity.
func (qe *QueryEngine) QuerySeverityRange(minSeverity string) []*wirelogs.LogStream {
	bm := qe.labelIndex.SeverityRange(minSeverity)
	ids := qe.labelIndex.ResolveStreamIDs(bm)
	return qe.resolveStreams(ids)
}

// LabelsForTemplate performs reverse traversal: template -> streams -> unique
// label values per key. Returns a map of label key -> sorted unique values.
func (qe *QueryEngine) LabelsForTemplate(templateID string) map[string][]string {
	bm := qe.labelIndex.StreamsForTemplate(templateID)
	ids := qe.labelIndex.ResolveStreamIDs(bm)
	return collectLabels(qe.streamByID, ids)
}

// collectLabels gathers unique label values per key from the given stream IDs.
func collectLabels(
	streamByID map[string]*wirelogs.LogStream,
	ids []string,
) map[string][]string {
	keyVals := make(map[string]map[string]struct{})
	for _, id := range ids {
		s, ok := streamByID[id]
		if !ok {
			continue
		}
		for k, v := range s.Labels {
			if _, exists := keyVals[k]; !exists {
				keyVals[k] = make(map[string]struct{})
			}
			keyVals[k][v] = struct{}{}
		}
	}
	result := make(map[string][]string, len(keyVals))
	for k, vs := range keyVals {
		vals := make([]string, 0, len(vs))
		for v := range vs {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		result[k] = vals
	}
	return result
}

// FilterByTimeRange returns chunks whose time range overlaps [start, end].
// The overlap test is: chunk.StartTime <= end && chunk.EndTime >= start.
func (qe *QueryEngine) FilterByTimeRange(
	chunks []*wirelogs.LogChunk,
	start, end time.Time,
) []*wirelogs.LogChunk {
	var out []*wirelogs.LogChunk
	for _, c := range chunks {
		if !c.StartTime.After(end) && !c.EndTime.Before(start) {
			out = append(out, c)
		}
	}
	return out
}

// TemplatesForLabels resolves: label filters -> streams -> chunks -> template
// IDs -> templates. Answers "what error patterns exist for service=api?"
func (qe *QueryEngine) TemplatesForLabels(
	filters map[string]string,
) []*wirelogs.LogTemplate {
	bm := qe.labelIndex.MatchLabels(filters)
	ids := qe.labelIndex.ResolveStreamIDs(bm)
	return qe.templatesFromStreams(ids)
}

// templatesFromStreams collects unique templates referenced by chunks of
// the given streams.
func (qe *QueryEngine) templatesFromStreams(streamIDs []string) []*wirelogs.LogTemplate {
	seen := make(map[string]struct{})
	var out []*wirelogs.LogTemplate
	for _, sid := range streamIDs {
		for _, c := range qe.chunksByStream[sid] {
			if _, ok := seen[c.TemplateID]; ok {
				continue
			}
			seen[c.TemplateID] = struct{}{}
			if t, ok := qe.templateByID[c.TemplateID]; ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// StatsFor returns pre-computed stats for a specific label key-value pair.
// Returns nil if the pair was not observed.
func (qe *QueryEngine) StatsFor(key, value string) *LabelValueStats {
	return qe.aggregation.StatsFor(key, value)
}

// TopK returns the top k values for a label key, sorted by error_count
// descending.
func (qe *QueryEngine) TopK(key string, k int) []LabelValueRanked {
	return qe.aggregation.TopK(key, k, "error_count")
}

// StreamCount reports how many unique streams the engine indexes. Exposed
// so lifecycle tooling (e.g., manage(list_logs)) can surface overview
// stats without cracking open the internal maps.
func (qe *QueryEngine) StreamCount() int {
	return len(qe.streamByID)
}

// Templates returns every indexed template. Exposed so callers that need
// to iterate the template set (e.g., the correlations mode that loads
// CORRELATES_WITH edges per template) don't have to re-query the
// underlying log graph.
func (qe *QueryEngine) Templates() []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, 0, len(qe.templateByID))
	for _, t := range qe.templateByID {
		out = append(out, t)
	}
	return out
}

// TemplateCount reports how many unique log templates the engine indexes.
func (qe *QueryEngine) TemplateCount() int {
	return len(qe.templateByID)
}

// resolveStreams converts hex stream IDs to *wirelogs.LogStream pointers.
func (qe *QueryEngine) resolveStreams(ids []string) []*wirelogs.LogStream {
	if len(ids) == 0 {
		return nil
	}
	out := make([]*wirelogs.LogStream, 0, len(ids))
	for _, id := range ids {
		if s, ok := qe.streamByID[id]; ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
