package searchengine

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// newTestEngine builds an engine over the mock format with merge effectively
// disabled (huge thresholds) so correctness tests are deterministic.
func newTestEngine(minSeg int) *SegmentedIndex[mockQuery, mockStats] {
	return New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     minSeg,
		DeletesPctAllowed:  2.0, // never triggers
		SegmentCountTarget: 1 << 30,
	})
}

func doc(id, content string) Document {
	return Document{ID: id, Fields: map[string]string{FieldContent: content}}
}

func searchIDs(hits []Hit) []ExternalID {
	ids := make([]ExternalID, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	sort.Strings(ids)
	return ids
}

func TestNewEngineEmpty(t *testing.T) {
	e := newTestEngine(1)
	defer e.Close()
	if got := e.Search(mockQuery{term: "anything"}, 5); got != nil {
		t.Fatalf("empty engine Search = %v, want nil", got)
	}
}

func TestOptionsDefaults(t *testing.T) {
	d := Options{}.withDefaults()
	if d.DeletesPctAllowed != defaultDeletesPctAllowed {
		t.Fatalf("DeletesPctAllowed default = %v, want %v", d.DeletesPctAllowed, defaultDeletesPctAllowed)
	}
	if d.MinSegmentDocs <= 0 || d.SegmentCountTarget <= 0 {
		t.Fatalf("zero defaults not filled: %+v", d)
	}
	caller := Options{DeletesPctAllowed: 0.5}.withDefaults()
	if caller.DeletesPctAllowed != 0.5 {
		t.Fatalf("caller DeletesPctAllowed overwritten: got %v", caller.DeletesPctAllowed)
	}
}

func TestAddCoalescing(t *testing.T) {
	e := newTestEngine(3)
	defer e.Close()

	// Sub-threshold: nothing sealed, nothing searchable.
	if err := e.Add([]Document{doc("a", "x"), doc("b", "x")}); err != nil {
		t.Fatal(err)
	}
	if got := e.Metrics().SegmentCount; got != 0 {
		t.Fatalf("after sub-threshold Add, SegmentCount = %d, want 0", got)
	}
	if hits := e.Search(mockQuery{term: "x"}, 10); len(hits) != 0 {
		t.Fatalf("sub-threshold docs searchable: %v", hits)
	}

	// Crossing the threshold seals exactly one segment with all docs live.
	if err := e.Add([]Document{doc("c", "x")}); err != nil {
		t.Fatal(err)
	}
	m := e.Metrics()
	if m.SegmentCount != 1 {
		t.Fatalf("after threshold, SegmentCount = %d, want 1", m.SegmentCount)
	}
	if m.DeadRatio != 0 {
		t.Fatalf("freshly sealed segment DeadRatio = %v, want 0", m.DeadRatio)
	}
	if got := searchIDs(e.Search(mockQuery{term: "x"}, 10)); len(got) != 3 {
		t.Fatalf("sealed segment search = %v, want 3 ids", got)
	}
}

func TestDeleteRouting(t *testing.T) {
	e := newTestEngine(1)
	defer e.Close()
	for _, id := range []string{"a", "b", "c"} {
		if err := e.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}

	e.Delete("b")
	got := searchIDs(e.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[a c]" {
		t.Fatalf("after Delete(b), search = %v, want [a c]", got)
	}

	// Unknown id is a no-op.
	before := e.Metrics()
	e.Delete("nonexistent")
	after := e.Metrics()
	if before.SegmentCount != after.SegmentCount {
		t.Fatalf("unknown delete changed segment count")
	}
}

func TestParallelSearchCorrectness(t *testing.T) {
	e := newTestEngine(1) // one segment per Add → many segments
	defer e.Close()

	var all []Document
	for i := range 50 {
		// vary term frequency so scores differ
		content := strings.Repeat("term ", i%5+1)
		d := doc(fmt.Sprintf("d%02d", i), content)
		all = append(all, d)
		if err := e.Add([]Document{d}); err != nil {
			t.Fatal(err)
		}
	}

	k := 10
	got := e.Search(mockQuery{term: "term"}, k)

	// Baseline: build all docs into ONE mock segment and search it.
	baseSeg, err := mockFormat{}.Build(all)
	if err != nil {
		t.Fatal(err)
	}
	want := baseSeg.Search(mockQuery{term: "term"}, mockStats{}, k, nil)

	if !sameHits(got, want) {
		t.Fatalf("parallel search != single-segment baseline\n got=%v\nwant=%v", got, want)
	}
}

// sameHits compares two top-k results by (Score, ID), order-significant after a
// stable sort by Score desc / ID asc.
func sameHits(a, b []Hit) bool {
	if len(a) != len(b) {
		return false
	}
	norm := func(h []Hit) []Hit {
		c := append([]Hit(nil), h...)
		sort.Slice(c, func(i, j int) bool {
			if c[i].Score != c[j].Score {
				return c[i].Score > c[j].Score
			}
			return c[i].ID < c[j].ID
		})
		return c
	}
	na, nb := norm(a), norm(b)
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

func TestMergeTopK(t *testing.T) {
	perSegment := [][]Hit{
		{{ID: "a", Score: 9}, {ID: "b", Score: 1}},
		{{ID: "c", Score: 7}, {ID: "d", Score: 5}},
		{{ID: "e", Score: 8}, {ID: "f", Score: 2}},
	}
	got := mergeTopK(perSegment, 3)

	// Brute-force baseline: sort the union, take top 3.
	var union []Hit
	for _, s := range perSegment {
		union = append(union, s...)
	}
	sort.Slice(union, func(i, j int) bool {
		if union[i].Score != union[j].Score {
			return union[i].Score > union[j].Score
		}
		return union[i].ID < union[j].ID
	})
	want := union[:3]
	if !sameHits(got, want) {
		t.Fatalf("mergeTopK = %v, want %v", got, want)
	}

	// k larger than total returns the whole union.
	if got := mergeTopK(perSegment, 100); len(got) != 6 {
		t.Fatalf("mergeTopK k>total len = %d, want 6", len(got))
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	src := newTestEngine(2)
	defer src.Close()
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := src.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	blobs := src.Export()
	if len(blobs) == 0 {
		t.Fatal("Export produced no blobs")
	}

	// Import into a fresh engine, tombstoning "b".
	dst := newTestEngine(2)
	defer dst.Close()
	if err := dst.Import(blobs, []ExternalID{"b"}); err != nil {
		t.Fatal(err)
	}

	got := searchIDs(dst.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[a c d]" {
		t.Fatalf("import round-trip search = %v, want [a c d] (b tombstoned)", got)
	}
}

// TestImportIsIdempotentBySegmentID pins the publishImport segment-ID dedup guard:
// importing the SAME blob twice leaves Export() length unchanged and a Search
// returns the doc exactly ONCE (not two result slots) — mergeTopK does not dedup
// docIDs, so a double-resident segment would otherwise duplicate the hit. A
// genuinely-distinct blob still appends (Export +1).
func TestImportIsIdempotentBySegmentID(t *testing.T) {
	src := newTestEngine(2)
	defer src.Close()
	for _, id := range []string{"a", "b"} {
		if err := src.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	blobB := src.Export()
	if len(blobB) != 1 {
		t.Fatalf("source Export = %d blobs, want 1", len(blobB))
	}

	dst := newTestEngine(2)
	defer dst.Close()
	if err := dst.Import(blobB, nil); err != nil {
		t.Fatal(err)
	}
	afterFirst := len(dst.Export())
	if afterFirst != 1 {
		t.Fatalf("after first Import, Export = %d, want 1", afterFirst)
	}

	// Re-import the SAME blob: idempotent — Export length unchanged.
	if err := dst.Import(blobB, nil); err != nil {
		t.Fatal(err)
	}
	if afterSecond := len(dst.Export()); afterSecond != afterFirst {
		t.Fatalf("re-import of the same blob changed Export = %d, want %d (idempotent by id)", afterSecond, afterFirst)
	}

	// A Search for a docID in B returns it exactly once (not two slots).
	got := searchIDs(dst.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[a b]" {
		t.Fatalf("after re-import, search = %v, want [a b] (each docID once, no duplicates)", got)
	}

	// A genuinely-distinct new blob still appends (Export +1).
	src2 := newTestEngine(2)
	defer src2.Close()
	for _, id := range []string{"c", "d"} {
		if err := src2.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	blobCD := src2.Export()
	if len(blobCD) != 1 {
		t.Fatalf("second source Export = %d blobs, want 1", len(blobCD))
	}
	if err := dst.Import(blobCD, nil); err != nil {
		t.Fatal(err)
	}
	if afterNew := len(dst.Export()); afterNew != afterFirst+1 {
		t.Fatalf("after importing a distinct blob, Export = %d, want %d (new blob appends)", afterNew, afterFirst+1)
	}
}

// TestResidentDocCount pins the read-side coverage accessor: 0 on a fresh engine,
// the summed sealed-segment doc count after Add+seal, and again after Import.
func TestResidentDocCount(t *testing.T) {
	e := newTestEngine(2) // MinSegmentDocs=2 → seals one segment per 2 docs
	defer e.Close()
	if got := e.ResidentDocCount(); got != 0 {
		t.Fatalf("fresh engine ResidentDocCount = %d, want 0", got)
	}

	// Add+seal 4 docs → 2 sealed segments of 2 docs each → resident 4.
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := e.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.ResidentDocCount(); got != 4 {
		t.Fatalf("after Add+seal of 4 docs, ResidentDocCount = %d, want 4", got)
	}

	// Import a 2-doc blob into a fresh engine → resident 2.
	dst := newTestEngine(2)
	defer dst.Close()
	src := newTestEngine(2)
	defer src.Close()
	for _, id := range []string{"e", "f"} {
		if err := src.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.Import(src.Export(), nil); err != nil {
		t.Fatal(err)
	}
	if got := dst.ResidentDocCount(); got != 2 {
		t.Fatalf("after Import of a 2-doc blob, ResidentDocCount = %d, want 2", got)
	}
}

func TestUnload(t *testing.T) {
	e := newTestEngine(1)
	defer e.Close()
	for _, id := range []string{"a", "b"} {
		if err := e.Add([]Document{doc(id, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	blobs := e.Export()
	// Unload the segment owning "a".
	var unloadID SegmentID
	set := e.set.Load()
	for _, entry := range set.entries {
		if _, ok := entry.members["a"]; ok {
			unloadID = entry.meta.ID
		}
	}
	_ = blobs
	e.Unload([]SegmentID{unloadID})
	got := searchIDs(e.Search(mockQuery{term: "x"}, 10))
	if fmt.Sprint(got) != "[b]" {
		t.Fatalf("after Unload, search = %v, want [b]", got)
	}
}

func TestLiveDocs(t *testing.T) {
	ld := newLiveDocs(130)
	if ld.LiveCount() != 130 {
		t.Fatalf("fresh LiveCount = %d, want 130", ld.LiveCount())
	}
	if !ld.Live(0) || !ld.Live(129) {
		t.Fatal("fresh bitset not all-live")
	}
	ld.Kill(64)
	ld.Kill(64) // idempotent
	if ld.Live(64) {
		t.Fatal("ordinal 64 still live after Kill")
	}
	if ld.DeadCount() != 1 {
		t.Fatalf("DeadCount = %d, want 1", ld.DeadCount())
	}
	if ld.Live(-1) || ld.Live(130) {
		t.Fatal("out-of-range ordinals must report dead")
	}
}

func TestLiveDocsFromTombstones(t *testing.T) {
	members := idSet{"a": 0, "b": 1, "c": 2}
	ld := newLiveDocsFromTombstones([]ExternalID{"b", "missing"}, members)
	if ld.Live(1) {
		t.Fatal("tombstoned member b should be dead")
	}
	if !ld.Live(0) || !ld.Live(2) {
		t.Fatal("non-tombstoned members should be live")
	}
	if ld.DeadCount() != 1 {
		t.Fatalf("DeadCount = %d, want 1", ld.DeadCount())
	}
}

func TestSegmentSetCOW(t *testing.T) {
	f := mockFormat{}
	seg1, _ := f.Build([]Document{doc("a", "x")})
	e1 := &segmentEntry[mockQuery, mockStats]{
		payload: seg1, live: newLiveDocs(1), members: idSet{"a": 0},
		meta: SegmentMeta{ID: "seg1", DocCount: 1},
	}
	base := newSegmentSet[mockQuery, mockStats](f, []*segmentEntry[mockQuery, mockStats]{e1})

	seg2, _ := f.Build([]Document{doc("b", "x")})
	e2 := &segmentEntry[mockQuery, mockStats]{
		payload: seg2, live: newLiveDocs(1), members: idSet{"b": 0},
		meta: SegmentMeta{ID: "seg2", DocCount: 1},
	}
	next := base.withAppended(f, e2)

	// The old snapshot is unmodified.
	if len(base.entries) != 1 {
		t.Fatalf("base mutated: %d entries", len(base.entries))
	}
	if _, ok := base.route["b"]; ok {
		t.Fatal("base route gained b — COW violated")
	}
	// The new snapshot has both.
	if len(next.entries) != 2 {
		t.Fatalf("next has %d entries, want 2", len(next.entries))
	}
	if next.route["a"] != "seg1" || next.route["b"] != "seg2" {
		t.Fatalf("next route wrong: %v", next.route)
	}
}

func TestMockFormat(t *testing.T) {
	f := mockFormat{}
	seg, err := f.Build([]Document{doc("a", "go go"), doc("b", "go"), doc("c", "rust")})
	if err != nil {
		t.Fatal(err)
	}
	// Search honors accept (exclude "b").
	hits := seg.Search(mockQuery{term: "go"}, mockStats{}, 10, func(id ExternalID) bool { return id != "b" })
	got := searchIDs(hits)
	if fmt.Sprint(got) != "[a]" {
		t.Fatalf("mock Search with accept = %v, want [a]", got)
	}

	// Encode/Decode round-trip.
	blob, err := seg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := f.Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	decIDs := dec.IDs()
	segIDs := seg.IDs()
	sort.Strings(decIDs)
	sort.Strings(segIDs)
	if fmt.Sprint(decIDs) != fmt.Sprint(segIDs) {
		t.Fatal("decoded segment IDs differ from built")
	}

	// Merge concatenates accept-kept rows, including on decoded inputs.
	merged, err := f.Merge(
		[]Segment[mockQuery, mockStats]{seg, dec},
		[]func(ExternalID) bool{
			func(id ExternalID) bool { return id == "a" },
			func(id ExternalID) bool { return id == "c" },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mergedIDs := merged.IDs()
	sort.Strings(mergedIDs)
	if fmt.Sprint(mergedIDs) != "[a c]" {
		t.Fatalf("Merge kept ids = %v, want [a c]", mergedIDs)
	}
}
