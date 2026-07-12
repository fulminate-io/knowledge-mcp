// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mockQuery / mockStats / mockRow / mockSegment / mockFormat mirror the engine's
// own test mock format (searchengine/mockformat_test.go) — re-declared here
// because test files are not importable across packages. A trivial in-memory
// SegmentFormat: indexes Document.Fields[FieldContent], scores by term frequency.

type mockQuery struct{ term string }

type mockStats struct{ totalDocs int }

type mockRow struct {
	ID      searchengine.ExternalID `json:"id"`
	Content string                  `json:"content"`
}

type mockSegment struct{ rows []mockRow }

type mockFormat struct{}

func (mockFormat) Name() string { return "mock" }

func (mockFormat) Build(docs []searchengine.Document) (searchengine.Segment[mockQuery, mockStats], error) {
	rows := make([]mockRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, mockRow{ID: d.ID, Content: d.Fields[searchengine.FieldContent]})
	}
	return &mockSegment{rows: rows}, nil
}

func (mockFormat) Decode(blob []byte) (searchengine.Segment[mockQuery, mockStats], error) {
	var rows []mockRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &mockSegment{rows: rows}, nil
}

func (mockFormat) Merge(segs []searchengine.Segment[mockQuery, mockStats], accept []func(searchengine.ExternalID) bool) (searchengine.Segment[mockQuery, mockStats], error) {
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

func (mockFormat) AggregateStats(segs []searchengine.Segment[mockQuery, mockStats]) mockStats {
	total := 0
	for _, s := range segs {
		total += len(s.(*mockSegment).rows)
	}
	return mockStats{totalDocs: total}
}

func (m *mockSegment) Search(q mockQuery, _ mockStats, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	var hits []searchengine.Hit
	for _, r := range m.rows {
		if accept != nil && !accept(r.ID) {
			continue
		}
		score := float64(strings.Count(r.Content, q.term))
		if score <= 0 {
			continue
		}
		hits = append(hits, searchengine.Hit{ID: r.ID, Score: score})
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

func (m *mockSegment) IDs() []searchengine.ExternalID {
	ids := make([]searchengine.ExternalID, 0, len(m.rows))
	for _, r := range m.rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func (m *mockSegment) Encode() ([]byte, error) { return json.Marshal(m.rows) }

// newMockEngine builds a SegmentedIndex over the mock format with MinSegmentDocs=1
// so every Add seals immediately (one segment per Add batch) — convenient for
// driving discrete segments in tests.
func newMockEngine() *searchengine.SegmentedIndex[mockQuery, mockStats] {
	return searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{MinSegmentDocs: 1})
}

// doc builds a content document.
func doc(id, content string) searchengine.Document {
	return searchengine.Document{ID: id, Fields: map[string]string{searchengine.FieldContent: content}}
}
