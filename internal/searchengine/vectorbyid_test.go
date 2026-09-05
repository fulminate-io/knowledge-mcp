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

// HeapBytes claims a per-row constant, deliberately non-zero, so the payload
// term of the residency model is drivable from a fixture rather than silently
// zero. Shares mockSegmentHeapBytesPerRow with the package's other double so
// the two cannot drift.
func (s *vecSegment) HeapBytes() int64 {
	return int64(len(s.rows)) * mockSegmentHeapBytesPerRow
}

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

func (vecFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	rows := make([]vecRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, vecRow{ID: d.ID, Vector: d.Vector})
	}
	return &vecSegment{rows: rows}, BuildReport{}, nil
}

func (vecFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	var rows []vecRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &vecSegment{rows: rows}, nil
}

func (vecFormat) MergeTo(dst MergeSink, segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool) (int64, error) {
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
	return writeMergedSegment(dst, &vecSegment{rows: merged})
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
	e := closeOnCleanup(t, New[mockQuery, mockStats](vecFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	}))
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

// TestVectorByIDDeclinesADeletedMember pins that VectorByID answers for the LIVE
// corpus, not the raw payload: an id whose live bit has been cleared resolves
// (nil,false) even though the segment still physically holds its vector.
//
// THE PHYSICAL-RESIDENCE ASSERTION IS THE POINT, and it is what makes this test
// measure liveness rather than eviction. A deleted id keeps its route and members
// entries and only loses its live bit — no indexed data mutates — so the row is
// still in the payload and the payload's own accessor still returns it. Without
// that leg, a merge that reclaimed the dead doc (or any change that dropped the
// row) would satisfy the (nil,false) assertion for a completely different reason
// and this test would pass while proving nothing. The engine's options make that
// unambiguous too: DeletesPctAllowed 2.0 is above the maximum possible dead ratio
// and SegmentCountTarget is 1<<30, so no merge is ever eligible here.
//
// KNOWN POSITIVE, same run: the three ids that were NOT deleted keep resolving
// byte-equal. A liveness consult that declined everything — or a lookup broken
// outright — is caught by that leg rather than read as success.
func TestVectorByIDDeclinesADeletedMember(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](vecFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0, // above any achievable dead ratio — merge never eligible.
		SegmentCountTarget: 1 << 30,
	}))
	defer e.Close()

	want := map[string][]byte{
		"a": {0x01, 0x02},
		"b": {0x03, 0x04},
		"c": {0x05, 0x06},
		"d": {0x07, 0x08},
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := e.Add([]Document{vecDoc(id, want[id])}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	// PRECONDITION: every id resolves before the delete, or the assertion below
	// would be satisfied by a lookup that never worked.
	for id := range want {
		if _, ok := e.VectorByID(id); !ok {
			t.Fatalf("PRECONDITION VectorByID(%s) ok=false — the id must resolve before the delete", id)
		}
	}

	const deleted = "c"
	e.Delete(deleted)

	// The row is STILL in the payload: Delete clears a bit, it does not rewrite the
	// segment. This is the control that makes the (nil,false) below mean "dead"
	// rather than "gone".
	set := e.set.Load()
	sid, routed := set.route[deleted]
	if !routed {
		t.Fatalf("PRECONDITION: %s lost its route entry — Delete must keep routing and clear only the live bit", deleted)
	}
	entry := set.entryByID(sid)
	if entry == nil {
		t.Fatalf("PRECONDITION: %s routes to a segment that is not resident", deleted)
	}
	if _, held := entry.payload.(*vecSegment).VectorByID(deleted); !held {
		t.Fatalf("PRECONDITION: the payload no longer holds %s — this test can no longer distinguish dead from reclaimed", deleted)
	}

	if got, ok := e.VectorByID(deleted); ok || got != nil {
		t.Fatalf("VectorByID(%s) = (%x, %v) after Delete, want (nil, false) — a deleted member's vector must not resolve", deleted, got, ok)
	}

	// KNOWN POSITIVE: exactly the deleted id went; the others are untouched.
	for _, id := range []string{"a", "b", "d"} {
		got, ok := e.VectorByID(id)
		if !ok {
			t.Fatalf("VectorByID(%s) ok=false after deleting %s — the consult must decline exactly the deleted id", id, deleted)
		}
		if !bytes.Equal(got, want[id]) {
			t.Fatalf("VectorByID(%s) = %x, want %x", id, got, want[id])
		}
	}
}

// TestVectorByIDGenericSafetyOnPayloadWithoutAccessor exercises the type-assert
// guard: a SegmentedIndex over a payload type WITHOUT a VectorByID method (the
// mock/bm25 shape) returns (nil,false) rather than panicking, even when the id is
// routed to a real segment. Fails-when-absent: a missing type-assert guard panics
// on the no-VectorByID payload.
func TestVectorByIDGenericSafetyOnPayloadWithoutAccessor(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	}))
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
