package searchengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
// why MergeTo works on decoded/pulled segments.
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

func (mockFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	rows := make([]mockRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, mockRow{ID: d.ID, Content: d.Fields[FieldContent]})
	}
	return &mockSegment{rows: rows}, BuildReport{}, nil
}

func (mockFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	var rows []mockRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &mockSegment{rows: rows}, nil
}

// MergeTo type-asserts each input to *mockSegment (the format owns its concrete
// type — the Lucene-style "read your own indexed internals" pattern; the Segment
// interface gains no accessor), keeps rows where accept[i](id) is true,
// concatenates the survivors into one all-live segment and writes its encoded
// bytes into dst. Works identically on built and decoded inputs because Decode
// rebuilds the same rows.
//
// A double has no streaming emitter to exercise, so the honest shape is the one
// that produces the same segment the engine would otherwise have been handed and
// reports its length.
//
// IT DOES NOT TRUNCATE, CLOSE OR UNLINK dst, matching the contract the interface
// states: the engine owns the destination. A double that tidied up after itself
// would hide an engine that forgot to.
func (mockFormat) MergeTo(dst MergeSink, segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool) (int64, error) {
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
	return writeMergedSegment(dst, &mockSegment{rows: merged})
}

// writeMergedSegment is the shared tail of every MergeTo in this package's
// doubles: encode the merged segment and place it at offset zero.
//
// A DOUBLE EMBEDDING mockFormat DELEGATES TO mockFormat.MergeTo RATHER THAN
// REACHING HERE, so its own gate runs first. See gateFormat.MergeTo for what goes
// wrong when the delegation is left implicit.
func writeMergedSegment(dst MergeSink, merged Segment[mockQuery, mockStats]) (int64, error) {
	blob, err := merged.Encode()
	if err != nil {
		return 0, err
	}
	n, err := dst.WriteAt(blob, 0)
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

// mergeMockSegments consolidates segs through a format's MergeTo and returns the
// merged Segment, doing on the test's behalf what the ENGINE does in production:
// create a destination, call MergeTo, size it from the reported length, decode.
//
// IT EXISTS FOR TESTS THAT WANT THE MERGED SEGMENT rather than its length,
// which the interface now reports instead of materializing. It takes the format
// as a parameter rather than hard-coding
// mockFormat{} so a double with its own injection is exercised through ITS
// MergeTo, not around it.
func mergeMockSegments(
	t *testing.T, f SegmentFormat[mockQuery, mockStats],
	segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool,
) (Segment[mockQuery, mockStats], error) {
	t.Helper()

	file, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("creating the merge destination: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("closing the merge destination: %v", err)
		}
	}()

	n, err := f.MergeTo(file, segs, accept)
	if err != nil {
		return nil, err
	}
	if err := file.Truncate(n); err != nil {
		t.Fatalf("sizing the merge destination to %d: %v", n, err)
	}
	blob, err := os.ReadFile(file.Name()) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("reading the merged segment back: %v", err)
	}
	return f.Decode(blob)
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

// mockSegmentHeapBytesPerRow is the per-row heap this double claims. It is a
// round, deliberately non-zero constant so a test can drive the payload term of
// the residency model to a KNOWN value and assert on it — a double returning
// zero would make the payload term indistinguishable from a missing one.
const mockSegmentHeapBytesPerRow int64 = 1000

func (m *mockSegment) HeapBytes() int64 {
	return int64(len(m.rows)) * mockSegmentHeapBytesPerRow
}
