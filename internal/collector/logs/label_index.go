// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"

	"github.com/RoaringBitmap/roaring/v2"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// LabelIndex is a Roaring bitmap inverted index mapping label key-value pairs
// to sets of wirelogs.LogStream uint32 IDs. It also maintains a reverse template index
// mapping template IDs to the streams they appear in.
type LabelIndex struct {
	// index maps labelKey -> labelValue -> bitmap of stream uint32 IDs.
	index map[string]map[string]*roaring.Bitmap

	// reverseTemplate maps templateID -> bitmap of stream uint32 IDs.
	// Built from wirelogs.LogChunk data: each chunk links a stream to a template.
	reverseTemplate map[string]*roaring.Bitmap

	// idMap resolves uint32 bitmap values back to hex stream IDs.
	idMap *StreamIDMap
}

// NewLabelIndex builds a LabelIndex from the provided streams, chunks, and
// templates. It indexes ALL labels (both LowCardLabels and HighCardLabels)
// and adds a synthetic "severity" label per stream derived from chunk templates.
func NewLabelIndex(
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	templates []*wirelogs.LogTemplate,
	idMap *StreamIDMap,
) *LabelIndex {
	li := &LabelIndex{
		index:           make(map[string]map[string]*roaring.Bitmap),
		reverseTemplate: make(map[string]*roaring.Bitmap),
		idMap:           idMap,
	}
	li.indexLabels(streams, idMap)
	li.indexSeverity(streams, chunks, templates, idMap)
	li.indexReverseTemplates(chunks, idMap)
	return li
}

// indexLabels adds every label key-value pair from each stream to the bitmap
// index. Both LowCardLabels and HighCardLabels are indexed.
func (li *LabelIndex) indexLabels(streams []*wirelogs.LogStream, idMap *StreamIDMap) {
	for _, s := range streams {
		uid := idMap.Add(s.ID)
		addLabels(li.index, s.LowCardLabels, uid)
		addLabels(li.index, s.HighCardLabels, uid)
	}
}

// addLabels inserts each key-value from labels into the index for the given
// stream uint32 ID.
func addLabels(index map[string]map[string]*roaring.Bitmap, labels map[string]string, uid uint32) {
	for k, v := range labels {
		vals, ok := index[k]
		if !ok {
			vals = make(map[string]*roaring.Bitmap)
			index[k] = vals
		}
		bm, ok := vals[v]
		if !ok {
			bm = roaring.New()
			vals[v] = bm
		}
		bm.Add(uid)
	}
}

// indexSeverity derives the maximum severity per stream from its chunks'
// templates and indexes it as a synthetic "severity" label.
func (li *LabelIndex) indexSeverity(
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	templates []*wirelogs.LogTemplate,
	idMap *StreamIDMap,
) {
	tmplMap := make(map[string]*wirelogs.LogTemplate, len(templates))
	for _, t := range templates {
		tmplMap[t.ID] = t
	}
	streamSev := deriveStreamSeverity(chunks, tmplMap)
	for _, s := range streams {
		sev, ok := streamSev[s.ID]
		if !ok {
			continue
		}
		uid, exists := idMap.Get(s.ID)
		if !exists {
			continue
		}
		vals, ok := li.index["severity"]
		if !ok {
			vals = make(map[string]*roaring.Bitmap)
			li.index["severity"] = vals
		}
		bm, ok := vals[sev]
		if !ok {
			bm = roaring.New()
			vals[sev] = bm
		}
		bm.Add(uid)
	}
}

// deriveStreamSeverity maps each stream ID to its maximum severity by scanning
// chunks and looking up their templates.
func deriveStreamSeverity(chunks []*wirelogs.LogChunk, tmplMap map[string]*wirelogs.LogTemplate) map[string]string {
	streamSev := make(map[string]string)
	for _, c := range chunks {
		t, ok := tmplMap[c.TemplateID]
		if !ok || t.Severity == "" {
			continue
		}
		cur, exists := streamSev[c.StreamID]
		if !exists || wirelogs.SeverityIndex(t.Severity) > wirelogs.SeverityIndex(cur) {
			streamSev[c.StreamID] = t.Severity
		}
	}
	return streamSev
}

// indexReverseTemplates builds the reverse template index from chunks,
// mapping each template ID to the bitmap of streams that reference it.
func (li *LabelIndex) indexReverseTemplates(chunks []*wirelogs.LogChunk, idMap *StreamIDMap) {
	for _, c := range chunks {
		uid, ok := idMap.Get(c.StreamID)
		if !ok {
			continue
		}
		bm, ok := li.reverseTemplate[c.TemplateID]
		if !ok {
			bm = roaring.New()
			li.reverseTemplate[c.TemplateID] = bm
		}
		bm.Add(uid)
	}
}

// MatchLabels returns the AND intersection of bitmaps for all filter
// key-value pairs. Returns nil if any filter has no matching bitmap.
func (li *LabelIndex) MatchLabels(filters map[string]string) *roaring.Bitmap {
	var result *roaring.Bitmap
	for k, v := range filters {
		vals, ok := li.index[k]
		if !ok {
			return roaring.New()
		}
		bm, ok := vals[v]
		if !ok {
			return roaring.New()
		}
		if result == nil {
			result = bm.Clone()
		} else {
			result.And(bm)
		}
	}
	if result == nil {
		return roaring.New()
	}
	return result
}

// SeverityRange returns the OR union of bitmaps for all severity levels at
// or above minSeverity. Uses wirelogs.SeverityIndex() for ordering:
// TRACE(0) < DEBUG(1) < INFO(2) < WARN(3) < ERROR(4) < CRITICAL(5).
func (li *LabelIndex) SeverityRange(minSeverity string) *roaring.Bitmap {
	minIdx := wirelogs.SeverityIndex(minSeverity)
	sevVals, ok := li.index["severity"]
	if !ok {
		return roaring.New()
	}
	result := roaring.New()
	for sev, bm := range sevVals {
		if wirelogs.SeverityIndex(sev) >= minIdx {
			result.Or(bm)
		}
	}
	return result
}

// LabelValues returns all indexed values for the given label key, sorted.
func (li *LabelIndex) LabelValues(key string) []string {
	vals, ok := li.index[key]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(vals))
	for v := range vals {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// LabelKeys returns all indexed label keys, sorted.
func (li *LabelIndex) LabelKeys() []string {
	out := make([]string, 0, len(li.index))
	for k := range li.index {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StreamsForTemplate returns the bitmap of stream IDs that reference the
// given template. Returns an empty bitmap if the template is not indexed.
func (li *LabelIndex) StreamsForTemplate(templateID string) *roaring.Bitmap {
	bm, ok := li.reverseTemplate[templateID]
	if !ok {
		return roaring.New()
	}
	return bm
}

// ResolveStreamIDs converts a Roaring bitmap of uint32 stream IDs back to
// their hex string representations via the StreamIDMap.
func (li *LabelIndex) ResolveStreamIDs(bm *roaring.Bitmap) []string {
	if bm == nil {
		return nil
	}
	arr := bm.ToArray()
	out := make([]string, 0, len(arr))
	for _, uid := range arr {
		if id := li.idMap.Resolve(uid); id != "" {
			out = append(out, id)
		}
	}
	return out
}
