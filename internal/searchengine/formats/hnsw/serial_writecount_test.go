// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// serial_writecount_test.go carries the two gates on the v3 emitter's WRITER, as
// opposed to on the bytes it emits: how many write calls an encode issues, and
// how many allocations the windows that bound them cost.
//
// THEY ARE TOGETHER BECAUSE THEY ARE THE TWO HALVES OF ONE PROPERTY. Coalescing
// writes is trivial if the buffer may be as large as the output; bounding the
// allocation is trivial if the writer never buffers. Either gate alone is
// satisfiable by the shape the other one forbids.

// writeCountingSink is a MergeSink that counts the WRITE CALLS an encode issues
// and forwards them to a real file.
//
// IT COUNTS CALLS, NOT BYTES WRITTEN, because the call count is the quantity
// that became the problem: against an *os.File each WriteAt is one pwrite(2)
// syscall, and the v3 writer issued one per encoded value — 17 for the header,
// five per node, one per neighbor, one per neighbor run, one per id-directory
// slot, and one each for the sentinel, the vector block and the footer. Bytes
// written did not change and were never the problem. The byte count is recorded
// anyway because coalescing MOVES it: the windows emit absorbed gaps as explicit
// zeros where the per-value writer left holes a sparse file reads back as zeros,
// so the sink observes more bytes while the file holds the same ones.
//
// IT FORWARDS TO A REAL FILE rather than standing in for one. A sink that
// swallowed the bytes would let this gate pass over an encode that produced
// nothing readable, and the checksum read-back inside the emitter — which reads
// the sink, not any buffer — would have nothing to read.
type writeCountingSink struct {
	f      *os.File
	writes int
	bytes  int64
}

func (c *writeCountingSink) WriteAt(p []byte, off int64) (int, error) {
	c.writes++
	c.bytes += int64(len(p))
	return c.f.WriteAt(p, off)
}

func (c *writeCountingSink) ReadAt(p []byte, off int64) (int, error) { return c.f.ReadAt(p, off) }

// v3StoreCount reports how many stores the emitter must issue for h.
//
// IT IS THE EXTERNAL EXPECTATION, and it has to be. A write-count bound compared
// against a number the writer itself produced would be an identity check that
// ratifies whatever the writer currently does, and decoding the produced segment
// to recover the count would ask the output for its own answer key. This walks
// the GRAPH the emitter walks and counts the store sites in encodeGraphV3To, so
// it is a property of the fixture and of the format, not of the buffering under
// test. The sibling format states the same discipline at
// bm25/merge_writecount_test.go:208-212.
//
// The terms are the store sites, in emission order: 17 header fields
// (serial.go:179-195), then per node an id offset, an id length, a max level, a
// layer index and the id bytes (:202-207), one layer-offset store per neighbor
// run (:211) and one arena store per neighbor (:214); then the single global
// sentinel (:220), one id-directory slot per node (:246-248), the vector block
// as ONE store when there is one to write (:256-257), and the footer CRC (:273).
func v3StoreCount(h *binaryGraph) int {
	const headerStores = 17
	const perNodeStores = 5

	stores := headerStores
	for i := range h.nodes {
		stores += perNodeStores
		stores += len(h.nodes[i].neighbors)
		for lv := range h.nodes[i].neighbors {
			stores += len(h.nodes[i].neighbors[lv])
		}
	}
	stores++               // the layer-offset sentinel that closes the last run
	stores += len(h.nodes) // the sorted id directory
	if len(h.nodes) > 0 && h.vecBytes > 0 {
		stores++ // the whole vector block, in one store
	}
	stores++ // the footer CRC
	return stores
}

// mergeWriteCountFixture runs a representative merge onto a real file and
// reports the write calls it issued, the stores the fixture demanded of it, and
// the segment's length.
//
// THE FIXTURE IS convergenceDocs RATHER THAN randomBuildItems, and that is not a
// preference. randomBuildItems and sizedBuildItems draw their ids from
// idForOrdinal, which formats three digits, so a fixture asked for more than
// 1,000 nodes silently collapses to 1,000 distinct ids and the merge dedupes the
// rest away. convergenceDocs formats four digits and qualifies them by seed, so
// four segments of 256 really do merge to 1,024 nodes.
//
// 1,024 NODES BECAUSE THE BOUND IS ABOUT SHAPE. At that size the blob spans
// several windows, so the bound below is a statement about writes scaling with
// FLUSHES rather than an artifact of an output that fits in one window and would
// be satisfied by any writer that buffered at all.
func mergeWriteCountFixture(t *testing.T) (writes, stores int, size int64) {
	t.Helper()

	const perSegment = 256
	const segments = 4

	segs := make([]searchengine.Segment[[]byte, struct{}], segments)
	for i := range segs {
		seg, _, err := Format{}.Build(convergenceDocs(perSegment, i+1))
		require.NoError(t, err)
		segs[i] = seg
	}
	accept := make([]func(searchengine.ExternalID) bool, segments)

	// The denominator comes from the SAME consolidation the emitter is about to
	// walk, taken before the emitter runs and independently of it.
	merged, err := mergeToGraph(segs, accept)
	require.NoError(t, err)
	stores = v3StoreCount(merged)

	f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	sink := &writeCountingSink{f: f}
	size, err = Format{}.MergeTo(sink, segs, accept)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))

	return sink.writes, stores, size
}

// maxWriteCallSlack is the bounded constant the write-call gate allows above
// ceil(blobLen / v3WriteRunBytes).
//
// IT IS DERIVED, AND THE DERIVATION IS TIGHT. Every call an encode makes is a
// whole-window flush, a partial tail, the footer's own flush, or a store too
// large to buffer. Whole-window flushes are already counted by the ceiling; the
// rest are at most one partial tail per open window (v3WriteRunSlots of them),
// one flush for the four footer bytes stored after the checksum read the sink
// back, and one pass-through for the vector block at widths where it exceeds a
// window. That is v3WriteRunSlots + 2.
//
// Measured on this fixture at 1,024 nodes: 9 calls against a ceiling of 6 whole
// windows, so the observed slack is 3 and the bound sits at 6.
//
// The headroom is sized to a REGRESSION rather than to noise: the failure worth
// catching is a window losing its stream affinity, which does not add two or
// three calls but takes the count back toward one per store — 73,625 on this
// fixture. A slack of 6 separates "the footer cost one more flush than it used
// to" from "the coalescing stopped working".
const maxWriteCallSlack = v3WriteRunSlots + 2

// TestMergeToCoalescesWritesIntoWindowFlushes is THE GATE on the v3 emitter's
// syscall count: write calls must scale with FLUSHES, not with encoded values.
//
// It is a deterministic assertion in the ordinary suite rather than a benchmark,
// and that split is deliberate. A benchmark does not run under `go test`, so a
// regression guarded only by one is guarded by a gate nobody executes; this runs
// on every package run and goes red the moment a per-value write is
// reintroduced.
func TestMergeToCoalescesWritesIntoWindowFlushes(t *testing.T) {
	writes, stores, size := mergeWriteCountFixture(t)
	windows := int((size + v3WriteRunBytes - 1) / v3WriteRunBytes)
	t.Logf("merge issued %d write calls for %d stores producing a %d-byte segment (%d whole windows)",
		writes, stores, size, windows)

	// The two vacuity controls, asserted before the bound they guard. Without the
	// first, a merge with a handful of stores would satisfy any ratio; without the
	// second, a merge that produced nothing would have a write count that
	// describes nothing.
	require.Greater(t, stores, 1000,
		"the fixture demanded only %d stores — too few for a write-count bound to say anything", stores)
	require.Positive(t, size, "the merge produced no output, so its write count describes nothing")

	require.LessOrEqualf(t, writes, windows+maxWriteCallSlack,
		"the merge issued %d write calls for a %d-byte segment, above %d whole windows plus a slack of %d. "+
			"The fixture demanded %d stores; a count drifting toward that is per-value writing, which means a "+
			"window stopped holding its stream — most likely a run that closes on flush instead of staying live "+
			"with its start advanced, so every stream falls through to round-robin eviction.",
		writes, size, windows, maxWriteCallSlack, stores)
}

// maxAllocsPerEncode is the pinned allocation COUNT for one encode.
//
// THE COUNT IS THE INSTRUMENT HERE, NOT THE BYTES, and the pair of gates is
// deliberate. TestEncodeGraphV3ToAllocatesNoOutputBuffer bounds the BYTES an
// encode allocates and proves they do not track the output; it cannot see how
// many allocations those bytes arrived in, so a writer that allocated its window
// once per store would leave it green as long as each allocation was small. This
// bounds the count and separates once-per-writer from once-per-store.
//
// THE NUMBER IS MEASURED, NOT CHOSEN. Steady state on this tree is 5.0 on both
// arms: the writer struct, its one backing array for every window, the
// id-directory ordinal slice, the reusable id buffer, and the fixed CRC
// read-back buffer. Before the windows landed it was 4.0 — the backing array is
// the whole of the increase, which is what "allocated once per writer" means as
// a number.
//
// THE HEADROOM IS THE GAP TO THE NEAREST REGRESSION, not a tolerance for jitter:
// testing.AllocsPerRun returns an exact count over 50 runs, not a timing sample,
// so there is no spread to absorb. Four failure shapes were MEASURED by
// installing exactly them in a scratch copy, and none of the four is in the
// tree. Both arms, narrow then wide:
//
//	a window allocated per store, not per writer:      18281.0  18281.0
//	windows grown by append, not sliced out of one
//	  pre-sized backing array:                            58.0     58.0
//	the same, with no window cap either, so a run
//	  grows toward its whole stream:                      59.0     59.0
//	one allocation per FLUSH (the window copied
//	  before it is written):                              11.0     11.0
//
// A bound of 7 therefore sits 2 above the steady state and 4 below the closest
// of the four, which is the window that separates "a refactor added an
// incidental allocation" from "the windows stopped being one allocation". THE
// LAST SHAPE IS WHY THE BOUND IS 7 AND NOT SOMETHING ROOMIER: at 11 it passed a
// bound of 12, which is what the first draft of this constant carried.
//
// WHAT THE CROSS-WIDTH EQUALITY BELOW DID AND DID NOT CATCH, stated because the
// measurement contradicted the expectation it was written on. It caught none of
// the four: every one of them reads EXACTLY EQUAL on the two arms. The reason is
// structural and worth knowing before trusting that leg — the two fixtures hold
// the same node count and differ ONLY in the vector block, which is a single
// store either way, buffered at the narrow width and passed straight through at
// the wide one, so nothing about the writer's work scales between them. The
// ceiling is what caught all four. The equality leg stays because it pins a real
// property, that the count does not move with vector width, but it is not the
// leg that is doing the work here and no reader should take it for one.
const maxAllocsPerEncode = 7

// TestEncodeGraphV3ToAllocatesItsWindowsOncePerWriter pins the allocation COUNT
// of one encode, at two vector widths.
//
// IT IS TOP LEVEL AND NEITHER IT NOR ITS SUBTESTS CALL t.Parallel(), which is a
// requirement of the instrument rather than a style choice: testing.AllocsPerRun
// panics outright if a parallel test is in flight (go1.27.0 src/testing/allocs.go:21-23),
// and this package runs 27 t.Parallel() calls across 8 files. alloc_test.go's
// TestSearchAllocationsAreBounded is shaped the same way for the same reason.
func TestEncodeGraphV3ToAllocatesItsWindowsOncePerWriter(t *testing.T) {
	narrowAllocs, narrowBlob := measureEncodeAllocCount(t, encodeFixtureNodes, encodeFixtureNarrowVec)
	t.Logf("narrow vectors: %.1f allocs writing a %d-byte blob (bound %d)",
		narrowAllocs, narrowBlob, maxAllocsPerEncode)

	wideAllocs, wideBlob := measureEncodeAllocCount(t, encodeFixtureNodes, encodeFixtureWideVec)
	t.Logf("wide vectors: %.1f allocs writing a %d-byte blob (bound %d)",
		wideAllocs, wideBlob, maxAllocsPerEncode)

	// The vacuity control, asserted before the bounds it guards: without it, two
	// fixtures that produced the same blob would satisfy the equality leg
	// trivially. It is the same control the bytes gate asserts at
	// serial_v3_test.go:237-240, over the bytes THIS encode wrote.
	require.GreaterOrEqualf(t, wideBlob, narrowBlob*2,
		"the wide fixture's blob (%d bytes) is not at least twice the narrow one's (%d bytes) — the equality below is vacuous",
		wideBlob, narrowBlob)

	// (a) EQUAL ACROSS THE WIDTHS: the count must not move with vector width.
	// See maxAllocsPerEncode for what this leg was measured to catch, which is less
	// than it looks — each of the four failure shapes installed there read exactly
	// equal on both arms, and the ceiling below is what caught them.
	// The delta is ZERO on purpose: testing.AllocsPerRun reports an exact count
	// over its runs, not a sample, so there is nothing here for a tolerance to
	// absorb and a non-zero one would only hide a real one-allocation divergence.
	require.InDeltaf(t, narrowAllocs, wideAllocs, 0,
		"the encode allocated %.1f times for a %d-byte blob and %.1f times for a %d-byte one — the count moved "+
			"with the output, which is what a buffer sized from the output looks like",
		narrowAllocs, narrowBlob, wideAllocs, wideBlob)

	// (b) AN ABSOLUTE CEILING, and this is the leg that does the work. Measured:
	// it is the only one of the two that caught any of the four installed failure
	// shapes. Both fixtures hold the same node count and the vector block is one
	// store at either width, so a writer that allocated a window per store reads
	// exactly equal on both arms — 18,281.0 against 18,281.0 — and only an
	// absolute bound separates that from once-per-writer.
	for _, arm := range []struct {
		name   string
		allocs float64
	}{{"narrow", narrowAllocs}, {"wide", wideAllocs}} {
		require.LessOrEqualf(t, arm.allocs, float64(maxAllocsPerEncode),
			"the %s encode allocates %.1f times, above the pinned bound of %d. The coalescing windows are "+
				"supposed to be ONE allocation for the whole writer — every window is a zero-length slice of one "+
				"pre-sized backing array — so a jump here means a window is being allocated per store or grown by "+
				"append instead of sliced.", arm.name, arm.allocs, maxAllocsPerEncode)
	}
}

// measureEncodeAllocCount reports the allocations one encodeGraphV3To makes, and
// the bytes it wrote.
//
// The destination is a FILE, not a sliceSink, for the reason measureEncodeToFile
// records at serial_v3_test.go:250-251: a slice destination carries the output
// in the caller's own allocation and the measurement becomes about the caller.
func measureEncodeAllocCount(t *testing.T, nodes, vecBytes int) (allocs float64, blobSize int64) {
	t.Helper()
	h := buildBinaryHNSWSerialDeterministic(
		sizedBuildItems(t, nodes, vecBytes), vecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

	f, err := os.Create(filepath.Join(t.TempDir(), "encoded.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	allocs = testing.AllocsPerRun(50, func() {
		n, err := encodeGraphV3To(f, h)
		// The in-closure control, following alloc_test.go:69-72: without it a
		// measurement over an encode that errored on its first store would report
		// the allocations of doing nothing, and report them as a pass.
		if err != nil {
			t.Fatalf("control: the measured encode failed: %v", err)
		}
		if n == 0 {
			t.Fatal("control: the measured encode wrote nothing, so this counts the allocations of doing nothing")
		}
		blobSize = n
	})
	return allocs, blobSize
}
