package searchengine

import (
	"encoding/json"
	"sort"
	"strings"
)

// mockQuery is a single search term matched against Document.Fields[FieldContent].
type mockQuery struct {
	term string
}

// mockStats is the corpus statistic the mock format exposes: total live+dead doc
// count across the set. It exercises AggregateStats and the engine's cached-S path.
type mockStats struct {
	totalDocs int
}

// mockRow is one indexed document row: the mock's "live indexed data". Build
// copies docs into rows; Decode reconstructs the SAME rows from JSON, so a
// decoded mockSegment is indistinguishable from a built one — which is exactly
// why Merge works on decoded/pulled segments.
type mockRow struct {
	ID      ExternalID `json:"id"`
	Content string     `json:"content"`
}

// mockSegment is the concrete Segment the mock format owns. Its rows slice is
// immutable after construction.
type mockSegment struct {
	rows []mockRow
}

// mockFormat is a trivial SegmentFormat used by every engine test.
type mockFormat struct{}

func (mockFormat) Name() string { return "mock" }

func (mockFormat) Build(docs []Document) (Segment[mockQuery, mockStats], error) {
	rows := make([]mockRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, mockRow{ID: d.ID, Content: d.Fields[FieldContent]})
	}
	return &mockSegment{rows: rows}, nil
}

func (mockFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	var rows []mockRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &mockSegment{rows: rows}, nil
}

// Merge type-asserts each input to *mockSegment (the format owns its concrete
// type — the Lucene-style "read your own indexed internals" pattern; the Segment
// interface gains no accessor), keeps rows where accept[i](id) is true, and
// concatenates the survivors into one all-live segment. Works identically on
// built and decoded inputs because Decode rebuilds the same rows.
func (mockFormat) Merge(segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool) (Segment[mockQuery, mockStats], error) {
	var merged []mockRow
	for i, s := range segs {
		ms := s.(*mockSegment)
		keep := accept[i]
		for _, r := range ms.rows {
			if keep == nil || keep(r.ID) {
				merged = append(merged, r)
			}
		}
	}
	return &mockSegment{rows: merged}, nil
}

func (mockFormat) AggregateStats(segs []Segment[mockQuery, mockStats]) mockStats {
	total := 0
	for _, s := range segs {
		total += len(s.(*mockSegment).rows)
	}
	return mockStats{totalDocs: total}
}

// Search linearly scans rows, honors the accept liveDocs filter, scores by term
// frequency (count of term occurrences in content), and returns the top-k hits
// sorted by score descending (ties broken by ID for determinism).
func (m *mockSegment) Search(q mockQuery, _ mockStats, k int, accept func(ExternalID) bool) []Hit {
	var hits []Hit
	for _, r := range m.rows {
		if accept != nil && !accept(r.ID) {
			continue
		}
		score := float64(strings.Count(r.Content, q.term))
		if score <= 0 {
			continue
		}
		hits = append(hits, Hit{ID: r.ID, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func (m *mockSegment) IDs() []ExternalID {
	ids := make([]ExternalID, 0, len(m.rows))
	for _, r := range m.rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func (m *mockSegment) Encode() ([]byte, error) {
	return json.Marshal(m.rows)
}
