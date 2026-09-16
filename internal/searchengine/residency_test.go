package searchengine

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// residencyFixture builds an index holding TWO sealed mock segments whose member
// ids have DELIBERATELY DIFFERENT LENGTHS. Equal-length ids would let a wrong
// model pass: with every id the same size, the per-member id-bytes term and the
// per-member constant term become indistinguishable, so a formula that dropped
// one and doubled the other would still land on the right total. content sets
// each row's payload text, which changes the ENCODED blob size without touching
// the member set — the lever the blob-independence assertion pulls.
func residencyFixture(t *testing.T, content string) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1}))
	require.NoError(t, e.Add([]Document{
		{ID: "a", Fields: map[string]string{FieldContent: content}},
		{ID: "bbbbbbbbbb", Fields: map[string]string{FieldContent: content}},
	}))
	require.NoError(t, e.Add([]Document{
		{ID: "ccc", Fields: map[string]string{FieldContent: content}},
	}))
	return e
}

// TestResidentHeapBytesCountsMembersNotBlob pins the residency model to the
// formula ResidentHeapBytes documents, term by term.
//
// THE EXPECTATION IS DERIVED BY HAND FROM THE FIXTURE, not by calling
// membersHeapBytes or routeHeapBytes. Computing it with the production helpers
// would be an identity check — the thing under test supplying its own answer key —
// and would pass just as happily with a helper wrong.
//
// THE RETAINED-BLOB TERM IS MEASURED OFF THE FIXTURE'S OWN BYTES rather than
// modeled, because that is what the term claims: a built payload retains exactly
// its encoder's output. It carries its own positive floor below, so a term that
// silently became zero cannot hide inside the total.
func TestResidentHeapBytesCountsMembersNotBlob(t *testing.T) {
	e := residencyFixture(t, "short")

	// Segment 1 holds ids "a" (1 byte) and "bbbbbbbbbb" (10 bytes) over 2
	// ordinals, so its bitset is one 64-bit word. Segment 2 holds "ccc" (3
	// bytes) over 1 ordinal, also one word.
	const (
		seg1Rows, seg2Rows = 2, 1
		seg1IDBytes        = 1 + 10
		seg2IDBytes        = 3
		seg1Members        = 2
		seg2Members        = 1
		bitsetWords        = 1 + 1 // one word per segment
	)
	// The retained encoder output of both sealed segments, read off the fixture.
	var retainedBlobs int64
	for _, entry := range e.set.Load().entries {
		blob, err := entry.payload.Encode()
		require.NoError(t, err)
		retainedBlobs += int64(len(blob))
	}

	// THE MAP TERM IS THE CAPACITY, NOT THE COUNT, and it is derived here by hand
	// from the fixture's own densities rather than by calling mapHeapBytes: two
	// members and one member each fit in a SINGLE group of eight slots, which is the
	// map form that carries no directory.
	const singleGroupSlots = 8
	perMap := int64(singleGroupSlots) * mapSlotBytes
	var structural int64
	for _, entry := range e.set.Load().entries {
		structural += entryStructuralBytes(entry)
	}

	want := int64(seg1Rows+seg2Rows)*mockSegmentHeapBytesPerRow + // payload term
		retainedBlobs + // the built payloads' retained encoder output
		int64(seg1IDBytes+seg2IDBytes) + // member id bytes
		2*perMap + // the two membership maps, one group each
		structural + // the entries' own structures
		int64(bitsetWords)*8 // liveness bitsets

	got := e.ResidentHeapBytes()
	require.Equal(t, want, got,
		"ResidentHeapBytes must equal the documented model exactly")

	// KNOWN-POSITIVE FLOORS. Each term is individually non-zero, so a total that
	// happened to match while a term was silently zero cannot pass unnoticed.
	require.Positive(t, int64(seg1Rows+seg2Rows)*mockSegmentHeapBytesPerRow,
		"payload term must be non-zero or this test cannot detect its removal")
	require.Positive(t, retainedBlobs,
		"retained-blob term must be non-zero or this test cannot detect its removal")
	require.Positive(t, perMap,
		"membership-map term must be non-zero or this test cannot detect its removal")
	require.Positive(t, structural,
		"entry-structure term must be non-zero or this test cannot detect its removal")

	// AND THE RECORD TERM IS ZERO HERE BY CONSTRUCTION — a sealed segment supersedes
	// nothing — which is why it has a case of its own below rather than a floor here.
	for _, entry := range e.set.Load().entries {
		require.Zero(t, recordHeapBytes(entry.record),
			"fixture control: a sealed segment carries no supersession record")
	}

	// THE ROUTE TERM IS ZERO HERE, AND THAT IS A PROPERTY RATHER THAN AN OMISSION:
	// an APPENDED segment joins the two-level route's TAIL and is resolved out of
	// its own members map, which membersHeapBytes already counts. The flat base — the
	// only thing routeHeapBytes counts — is still empty at two appends.
	// TestResidentHeapBytesCountsTheFlatRoute covers the flattened set.
	require.Empty(t, e.set.Load().base,
		"fixture control: an appended set must still route out of the tail, or the route term below is not what is being asserted")
}

// TestResidentHeapBytesCountsTheFlatRoute pins the route term on a set that IS
// flattened — the shape a pool loaded from L2 has, since Import publishes through
// newSegmentSet and builds the flat route over every entry.
//
// It is a separate case because the two-level route makes this the OTHER arm of one
// behaviour: an appended set pays its routing through the members maps (counted as
// term 3) and a flattened one pays it through the base map (term 5). A model that
// counted neither, or counted the base for a tail entry too, passes one of these
// cases and fails the other.
func TestResidentHeapBytesCountsTheFlatRoute(t *testing.T) {
	src := residencyFixture(t, "short")
	blobs := src.Export()
	require.Len(t, blobs, 2, "fixture control: the source must hold two segments")

	dst := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1 << 20}))
	// Release non-nil marks these as mapping-backed by entryFromDecoded's documented
	// rule, so the retained-blob term is zero and the route term is not hidden inside
	// it. keepAlive is what a real Export-then-Import carries; both fixtures below
	// hand in an explicit release instead, which is the L2 load path's shape.
	imported := make([]SegmentBlob, 0, len(blobs))
	for _, b := range blobs {
		imported = append(imported, SegmentBlob{ID: b.ID, Bytes: b.Bytes, Release: func() {}})
	}
	require.NoError(t, dst.Import(imported, nil))

	set := dst.set.Load()
	require.Len(t, set.base, 3, "fixture control: the flat route must hold the three distinct ids")
	require.Equal(t, len(set.entries), set.baseN,
		"fixture control: an imported set must be fully flattened, or this asserts the wrong arm")

	const (
		distinctIDs = 3
		idBytes     = 1 + 10 + 3
		rows        = 3
		bitsetWords = 2
		groupSlots  = 8 // both membership maps fit one group
	)
	var structural int64
	for _, entry := range set.entries {
		structural += entryStructuralBytes(entry)
	}
	want := int64(rows)*mockSegmentHeapBytesPerRow +
		int64(idBytes) +
		2*int64(groupSlots)*mapSlotBytes +
		structural +
		int64(bitsetWords)*8 +
		int64(distinctIDs)*routeEntryOverheadBytes

	require.Equal(t, want, dst.ResidentHeapBytes(),
		"a flattened set must count its flat route once per distinct id")
	require.Positive(t, int64(distinctIDs)*routeEntryOverheadBytes,
		"route term must be non-zero or this test cannot detect its removal")
}

// TestResidentHeapBytesIgnoresBlobSizeWhenMapped is the MAPPED half of the
// blob-independence property, and it is the half that must survive: growing the
// encoded blob by two orders of magnitude while holding the member set identical
// must not move the number at all, because those bytes are page cache — evictable,
// shared between processes and invisible to the garbage collector.
//
// It is the surviving half of a pin that used to make this claim for EVERY payload.
// That was true when nothing distinguished a built payload from a loaded one, and
// it is what let a heap-backed pool under-report by 10x; the built half is now
// TestResidentHeapBytesTracksBlobSizeWhenHeapBacked, and deleting this case instead
// of splitting it would have removed the only guard on the mapped side.
func TestResidentHeapBytesIgnoresBlobSizeWhenMapped(t *testing.T) {
	small := importedFixture(t, "x")
	large := importedFixture(t, strings.Repeat("x", 100_000))

	// CONTROL: the fixtures really do differ in encoded size. Without this the
	// equality below could hold because both blobs were the same all along.
	smallBlob, err := small.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	largeBlob, err := large.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	require.Greater(t, len(largeBlob), 50*len(smallBlob),
		"fixture control: the large fixture must encode far bigger, or this test proves nothing")

	require.Equal(t, small.ResidentHeapBytes(), large.ResidentHeapBytes(),
		"a mapped payload's blob is page cache: the heap model must be independent of its size")
}

// TestResidentHeapBytesTracksBlobSizeWhenHeapBacked is the BUILT half, and it is
// requirement 2 in one assertion: a sealed segment retains its encoder output, so
// the budget must grow with it. The previous pin asserted the opposite for this
// provenance, which is exactly how a 10x under-count shipped with a green suite.
func TestResidentHeapBytesTracksBlobSizeWhenHeapBacked(t *testing.T) {
	small := residencyFixture(t, "x")
	large := residencyFixture(t, strings.Repeat("x", 100_000))

	smallBlob, err := small.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	largeBlob, err := large.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	require.Greater(t, len(largeBlob), 50*len(smallBlob),
		"fixture control: the large fixture must encode far bigger, or this test proves nothing")

	// THE EXPECTATION IS THE FIXTURE'S OWN LEVER, not the model's arithmetic: the
	// two fixtures differ only in content, so the model must differ by at least the
	// bytes that difference put on the heap.
	grew := large.ResidentHeapBytes() - small.ResidentHeapBytes()
	require.GreaterOrEqual(t, grew, int64(len(largeBlob)-len(smallBlob)),
		"the model must grow by at least the extra encoder output a heap-backed payload retains")
}

// importedFixture is residencyFixture's mapping-backed twin: the same documents,
// published through the IMPORT path with a blob that owns its mapping, which is
// how a pool loaded from the L2 disk cache arrives.
func importedFixture(t *testing.T, content string) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	src := residencyFixture(t, content)
	dst := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1 << 20}))
	var imported []SegmentBlob
	for _, b := range src.Export() {
		imported = append(imported, SegmentBlob{ID: b.ID, Bytes: b.Bytes, Release: func() {}})
	}
	require.NoError(t, dst.Import(imported, nil))
	return dst
}

// TestResidentHeapBytesCountsTheSupersessionRecord is the term a corpus of one
// SHAPE cannot see. A merge-disabled code corpus carries no supersession records
// at all, so a model fitted on one reads within 3% while the same model reads 42%
// under on a rebuilt knowledge corpus, where a minority of blobs each carry
// hundreds of recorded ids. The ids are COPIED onto the heap by readIDs — a string
// conversion, because the record outlives the envelope it was decoded from — so
// they are the engine's heap and the budget must see them.
func TestResidentHeapBytesCountsTheSupersessionRecord(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1 << 20}))

	// Two consolidated blobs: one carrying a record, one carrying none, so the
	// difference between them is exactly the term.
	src := residencyFixture(t, "short")
	exported := src.Export()
	require.Len(t, exported, 2, "fixture control: two source segments")

	superseded := []SegmentID{
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
	}
	cohort := []SegmentID{
		"3333333333333333333333333333333333333333333333333333333333333333",
	}
	stamped := SegmentBlob{
		ID: exported[0].ID, Bytes: exported[0].Bytes, Release: func() {},
		Envelope: encodeSupersessionPrefix(supersessionRecord{Superseded: superseded, Cohort: cohort}),
	}
	require.NotEmpty(t, stamped.Envelope, "fixture control: the stamped blob must carry an envelope")

	// TWO ENGINES OVER THE SAME BYTES, differing in the record and in NOTHING else:
	// same payload, same members, same structures. Importing a second, different
	// segment into one engine would have compared two corpora and the difference
	// would have swamped the term.
	bare := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1 << 20}))
	require.NoError(t, bare.Import([]SegmentBlob{
		{ID: exported[0].ID, Bytes: exported[0].Bytes, Release: func() {}},
	}, nil))
	withoutRecord := bare.ResidentHeapBytes()
	require.NoError(t, e.Import([]SegmentBlob{stamped}, nil))
	withRecord := e.ResidentHeapBytes()

	// THE EXPECTATION IS DERIVED FROM THE FIXTURE'S OWN IDS, by hand: three 64-byte
	// ids, each costing its bytes plus the string header its slice element holds.
	const recordIDs = 3
	wantRecordTerm := int64(recordIDs) * (64 + segmentIDHeaderBytes)

	entry := e.set.Load().entryByID(stamped.ID)
	require.NotNil(t, entry, "the stamped segment must be resident")
	require.Len(t, entry.record.Superseded, 2, "control: the record survived the decode")
	require.Equal(t, wantRecordTerm, recordHeapBytes(entry.record),
		"the record term must count every superseded and cohort id's bytes and header")

	require.Equal(t, wantRecordTerm, withRecord-withoutRecord,
		"the ONLY difference between the two engines is the record, so the model must differ by exactly its bytes")
	require.Positive(t, wantRecordTerm,
		"record term must be non-zero or this test cannot detect its removal")
}

// TestModelConstantsMatchTheRuntimesMapCost is the CALIBRATION guard for the two
// numbers in this file that are measurements of the Go runtime rather than of
// anything in this package.
//
// mapSlotBytes and routeEntryOverheadBytes describe a map slot's cost in the
// runtime this binary was built with. A toolchain that changes the map layout
// moves them under a model that still compiles and still passes every behavioral
// test, and the corpus instrument that would catch it needs a corpus and skips by
// name without one. This row needs nothing: it builds the two map shapes the model
// describes, over keys and values that are already allocated, and compares the
// measured per-slot and per-entry costs against the constants.
func TestModelConstantsMatchTheRuntimesMapCost(t *testing.T) {
	const tolerance = 0.20

	// The membership shape: one map per segment, at the density the reference
	// corpora carry, with its ids allocated before the baseline is taken.
	const maps, perMap = 2000, 17
	ids := make([][]ExternalID, maps)
	for m := range maps {
		row := make([]ExternalID, perMap)
		for i := range perMap {
			row[i] = "0123456789abcdef0123456789abcd" + strconv.Itoa(m) + "-" + strconv.Itoa(i)
		}
		ids[m] = row
	}
	base := liveHeapNow()
	built := make([]idSet, 0, maps)
	for m := range maps {
		mm := make(idSet, perMap)
		for ord, id := range ids[m] {
			mm[id] = ord
		}
		built = append(built, mm)
	}
	measured := liveHeapNow() - base
	slots, _ := mapSlotsFor(perMap)
	modeled := int64(maps) * mapHeapBytes(perMap)
	t.Logf("membership maps: n=%d slots=%d measured=%.1fB/slot modeled=%.1fB/slot",
		perMap, slots, float64(measured)/float64(int64(maps)*slots), float64(modeled)/float64(int64(maps)*slots))
	if drift := relativeDrift(modeled, measured); drift > tolerance {
		t.Errorf("mapSlotBytes=%d models %d bytes for %d maps of %d entries, but they cost %d (%.0f%% off) — "+
			"this constant is a measurement of the runtime's map layout and the runtime has moved",
			mapSlotBytes, modeled, maps, perMap, measured, drift*100)
	}
	runtime.KeepAlive(built)
	runtime.KeepAlive(ids)

	// The route shape: ONE map over every distinct id, string keys and string
	// values, at a corpus-sized count.
	const routeIDs = 115981
	keys := make([]ExternalID, routeIDs)
	for i := range routeIDs {
		keys[i] = "0123456789abcdef0123456789ab" + strconv.Itoa(i)
	}
	segIDs := make([]SegmentID, 0, 2000)
	for i := range 2000 {
		segIDs = append(segIDs, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"+strconv.Itoa(i))
	}
	routeBase := liveHeapNow()
	route := make(map[ExternalID]SegmentID, 2000*4)
	for i, k := range keys {
		route[k] = segIDs[i%len(segIDs)]
	}
	routeMeasured := liveHeapNow() - routeBase
	routeModeled := int64(routeIDs) * routeEntryOverheadBytes
	t.Logf("route map: n=%d measured=%.1fB/id modeled=%dB/id",
		routeIDs, float64(routeMeasured)/routeIDs, routeEntryOverheadBytes)
	if drift := relativeDrift(routeModeled, routeMeasured); drift > tolerance {
		t.Errorf("routeEntryOverheadBytes=%d models %d bytes for %d route ids, but they cost %d (%.0f%% off)",
			routeEntryOverheadBytes, routeModeled, routeIDs, routeMeasured, drift*100)
	}
	runtime.KeepAlive(route)
	runtime.KeepAlive(keys)
	runtime.KeepAlive(segIDs)
}

// liveHeapNow reads live heap bytes after two forced collections.
func liveHeapNow() int64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}

// relativeDrift is |modeled-measured|/measured, or 0 when nothing was measured.
func relativeDrift(modeled, measured int64) float64 {
	if measured <= 0 {
		return 0
	}
	d := float64(modeled-measured) / float64(measured)
	if d < 0 {
		return -d
	}
	return d
}

// TestResidentHeapBytesCountsTheBaseDropSet is the eighth term: a snapshot that
// has had base entries consolidated out from under its shared route carries a
// record of them, and that record is heap the model must see.
//
// IT MATTERS MORE THAN ITS SIZE. The set is bounded at routeDropLimit, so the
// bytes are small — but a term the model cannot see is how a budget starts
// under-reading, and this one holds the LAST reference to its ids: a dropped
// segment has left the entry slice, so nothing else in the snapshot keeps those
// bytes alive.
func TestResidentHeapBytesCountsTheBaseDropSet(t *testing.T) {
	f := mockFormat{}
	entries := []*segmentEntry[mockQuery, mockStats]{
		// "gone" is held by BOTH, so the replacement below has a real answer to
		// pre-resolve: the index keys only ids an older flattened entry still holds.
		routeEntry(t, "drop-b0", []ExternalID{"keep-a", "gone"}, "gone"),
		routeEntry(t, "drop-b1", []ExternalID{"keep-b", "gone"}, "gone"),
	}
	set := newSegmentSet(f, entries)
	require.Empty(t, set.dropped, "fixture control: a flat snapshot must carry no drop set")
	require.Empty(t, set.staleIndex, "fixture control: a flat snapshot must carry no pre-resolved answers")

	next := set.withReplaced(f, map[SegmentID]bool{"drop-b1": true}, routeEntry(t, "drop-out", []ExternalID{"keep-b"}))
	require.NotEmpty(t, next.dropped, "PRECONDITION: the replacement must have recorded a drop, or this row asserts nothing")

	e := closeOnCleanup(t, New[mockQuery, mockStats](f, Options{MinSegmentDocs: 1 << 20}))
	e.set.Store(next)
	withDrops := e.ResidentHeapBytes()

	// The SAME snapshot with the drop set removed and nothing else changed, so the
	// difference between the two readings is exactly this term and nothing else.
	bare := *next
	bare.dropped = nil
	e.set.Store(&bare)
	without := e.ResidentHeapBytes()

	require.Equal(t, droppedHeapBytes(next.dropped), withDrops-without,
		"the model must report the drop set's ids and its map structure, and nothing else")
	require.Positive(t, droppedHeapBytes(next.dropped),
		"the term must be non-zero here or this test cannot detect its removal")

	// AND THE PRE-RESOLVED STALE ANSWERS, the ninth term. The replacement above left
	// a dead member behind with no surviving holder, so the index holds a key for it
	// — and that key's id bytes are held alive by this map alone, every other holder
	// having left the set.
	require.NotEmpty(t, next.staleIndex,
		"PRECONDITION: the replacement must have pre-resolved a stale hit, or the term below is not exercised")
	bareStale := *next
	bareStale.staleIndex = nil
	e.set.Store(&bareStale)
	withoutStale := e.ResidentHeapBytes()
	e.set.Store(next)
	require.Equal(t, staleIndexHeapBytes(next.staleIndex), e.ResidentHeapBytes()-withoutStale,
		"the model must report the stale index's ids and its map structure, and not its values, which are "+
			"pointers to entries this same walk already charges in full")
	require.Positive(t, staleIndexHeapBytes(next.staleIndex),
		"the term must be non-zero here or this test cannot detect its removal")

	// AND A FLATTEN RELEASES BOTH: the rebuild folds the surviving entries into a new
	// base map, so there is nothing left for a base hit to be checked against and
	// nothing left to pre-resolve.
	flat := newSegmentSet(f, next.entries)
	require.Empty(t, flat.dropped, "a flatten must release the drop set")
	require.Empty(t, flat.staleIndex, "a flatten must release the pre-resolved stale answers")
}
