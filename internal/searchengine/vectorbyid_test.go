package searchengine

import (
	"bytes"
	"encoding/json"
	"testing"
)

// vecRow is one row of the vector-bearing mock: an id and its stored vector. It
// is the in-package stand-in for the hnsw payload (the searchengine package cannot
// import hnsw without a cycle), carrying just enough to exercise the route-map walk
// and the by-id stored-vector read that SegmentedIndex.VectorByID resolves through.
type vecRow struct {
	ID     ExternalID `json:"id"`
	Vector []byte     `json:"vector"`
}

// vecSegment is a Segment[mockQuery, mockStats] payload that ALSO carries a
// VectorByID accessor — the shape SegmentedIndex.VectorByID type-asserts to. Its
// Search is a no-op (these tests exercise vector resolution, not ranking).
type vecSegment struct {
	rows []vecRow
}

func (s *vecSegment) Search(_ mockQuery, _ mockStats, _ int, _ func(ExternalID) bool) []Hit {
	return nil
}

func (s *vecSegment) IDs() []ExternalID {
	ids := make([]ExternalID, 0, len(s.rows))
	for _, r := range s.rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func (s *vecSegment) Encode() ([]byte, error) { return json.Marshal(s.rows) }

// VectorByID is the by-id stored-vector accessor SegmentedIndex.VectorByID reaches
// via runtime type-assert. Returns (nil,false) for an id this segment does not hold.
func (s *vecSegment) VectorByID(externalID string) ([]byte, bool) {
	for _, r := range s.rows {
		if r.ID == externalID {
			return r.Vector, true
		}
	}
	return nil, false
}

// vecFormat builds vecSegments. MinSegmentDocs=1 in the test seals one segment per
// Add, so successive Adds produce MULTIPLE sealed segments and the route map must
// be walked to find an id held in a non-first segment.
type vecFormat struct{}

func (vecFormat) Name() string { return "vec" }

func (vecFormat) Build(docs []Document) (Segment[mockQuery, mockStats], error) {
	rows := make([]vecRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, vecRow{ID: d.ID, Vector: d.Vector})
	}
	return &vecSegment{rows: rows}, nil
}

func (vecFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	var rows []vecRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &vecSegment{rows: rows}, nil
}

func (vecFormat) Merge(segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool) (Segment[mockQuery, mockStats], error) {
	var merged []vecRow
	for i, s := range segs {
		vs := s.(*vecSegment)
		keep := accept[i]
		for _, r := range vs.rows {
			if keep == nil || keep(r.ID) {
				merged = append(merged, r)
			}
		}
	}
	return &vecSegment{rows: merged}, nil
}

func (vecFormat) AggregateStats([]Segment[mockQuery, mockStats]) mockStats { return mockStats{} }

func vecDoc(id string, vec []byte) Document { return Document{ID: id, Vector: vec} }

// TestVectorByIDRoutesToOwningSegment adds docs across MULTIPLE sealed segments
// (MinSegmentDocs=1 → one segment per Add) and asserts SegmentedIndex.VectorByID
// resolves an id held in a NON-FIRST segment — proving it walks set.route →
// entryByID rather than reading entries[0]. An id present in no segment returns
// (nil,false). Fails-when-absent: a single-segment-only lookup misses the
// non-first id.
func TestVectorByIDRoutesToOwningSegment(t *testing.T) {
	e := New[mockQuery, mockStats](vecFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	})
	defer e.Close()

	want := map[string][]byte{
		"a": {0x01, 0x02},
		"b": {0x03, 0x04},
		"c": {0x05, 0x06},
		"d": {0x07, 0x08},
	}
	// One Add per id → four distinct sealed segments. "d" lands in the last one.
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := e.Add([]Document{vecDoc(id, want[id])}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	for id, vec := range want {
		got, ok := e.VectorByID(id)
		if !ok {
			t.Fatalf("VectorByID(%s) ok=false, want true (id is in a sealed segment)", id)
		}
		if !bytes.Equal(got, vec) {
			t.Fatalf("VectorByID(%s) = %x, want %x", id, got, vec)
		}
	}

	if got, ok := e.VectorByID("missing"); ok || got != nil {
		t.Fatalf("VectorByID(absent) = (%x, %v), want (nil, false)", got, ok)
	}
}

// TestVectorByIDGenericSafetyOnPayloadWithoutAccessor exercises the type-assert
// guard: a SegmentedIndex over a payload type WITHOUT a VectorByID method (the
// mock/bm25 shape) returns (nil,false) rather than panicking, even when the id is
// routed to a real segment. Fails-when-absent: a missing type-assert guard panics
// on the no-VectorByID payload.
func TestVectorByIDGenericSafetyOnPayloadWithoutAccessor(t *testing.T) {
	e := New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	})
	defer e.Close()

	if err := e.Add([]Document{doc("a", "content")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// "a" IS routed (it is in a sealed segment) — the entry resolves, but the
	// mockSegment payload has no VectorByID, so the type-assert must fail cleanly.
	if got, ok := e.VectorByID("a"); ok || got != nil {
		t.Fatalf("VectorByID over no-accessor payload = (%x, %v), want (nil, false) — must not panic", got, ok)
	}
}
