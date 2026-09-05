// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_armorder_test.go — the ROUTING ORDER of mergeWriter.store's arms, which
// is what keeps every coalescing window inside the single backing array
// newMergeWriter hands out.
//
// THE ORDER IS LOAD-BEARING, NOT A STYLE CHOICE. A store larger than a
// window that arrives contiguous with a live run matches BOTH the oversize
// pass-through arm and the contiguous-extend arm, and whichever is tested first
// takes it. Extend-first appends len(b) bytes onto a slice whose capacity is
// exactly mergeRunBytes, so append allocates a fresh array and that window
// leaves the backing array for the rest of the writer's life — flushRun keeps
// the reallocated array when it does r.buf = r.buf[:0]. Measured on this
// package's own suite before the order was fixed: 37 stores over the window per
// suite run, 17 of them contiguous with a live run, 15 fresh reallocations
// totaling 271,616 bytes, and up to 2 of the 8 slots detached at once.
// Oversize-first routes the same store to one pass-through write and touches no
// window at all.
//
// THE PRODUCTION PATH REACHES IT, which is why the last test here merges a
// corpus instead of hand-building a store sequence. A merged posting run is 6
// bytes per document frequency (appendPostings) and a merged per-field
// document-length array is 4 bytes per document (writeMergePrefix), against a
// threshold of mergeRunBytes-mergeRunMaxGap = 8,184 bytes — so a term carried by
// 1,365 documents, or a corpus of 2,047 documents, crosses it on the default
// dictBlocked encoding with no fixture tricks at all.

// sinkWrite is one WriteAt call's coordinates.
type sinkWrite struct {
	off int64
	n   int
}

// armOrderSink is a MergeSink that records WHERE and HOW MUCH every write call
// wrote, and forwards it to a real file.
//
// IT RECORDS COORDINATES WHERE countingSink RECORDS ONLY A COUNT, and the two
// are not redundant. That sink answers "how many syscalls did the merge issue",
// which is the question the coalescing exists for. These tests ask WHICH ARM
// issued a given write, and that is visible only in the individual call: a
// pass-through write carries the store's own offset and its own length, while a
// flushed window carries the run's start and whatever the run had buffered.
//
// IT FORWARDS TO A REAL FILE for the same reason countingSink does: a sink that
// swallowed the bytes would let a merge that produced nothing readable satisfy
// every assertion below.
type armOrderSink struct {
	f      *os.File
	writes []sinkWrite
}

func (s *armOrderSink) WriteAt(p []byte, off int64) (int, error) {
	s.writes = append(s.writes, sinkWrite{off: off, n: len(p)})
	return s.f.WriteAt(p, off)
}

func (s *armOrderSink) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }

// newArmOrderSink opens a test-owned file behind a recording sink.
func newArmOrderSink(t *testing.T) *armOrderSink {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "armorder.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	return &armOrderSink{f: f}
}

// requireEveryWindowOnTheBackingArray asserts the invariant newMergeWriter's own
// comment states: every window is a slice of the one backing array, so every
// window's capacity is still exactly mergeRunBytes.
func requireEveryWindowOnTheBackingArray(t *testing.T, w *mergeWriter, when string) {
	t.Helper()
	for i := range w.runs {
		require.Equal(t, mergeRunBytes, cap(w.runs[i].buf),
			"%s: run %d left the single backing array — its cap is %d, not mergeRunBytes (%d). "+
				"A store larger than the window reached the contiguous-extend arm and append reallocated",
			when, i, cap(w.runs[i].buf), mergeRunBytes)
	}
}

// acceptEvery keeps every document of every input live.
func acceptEvery(n int) []func(searchengine.ExternalID) bool {
	out := make([]func(searchengine.ExternalID) bool, n)
	for i := range out {
		out[i] = func(searchengine.ExternalID) bool { return true }
	}
	return out
}

// realMergeMachinery runs the genuine merge machinery over ins — the same calls
// streamMergeToFile makes, in the same order — and returns the emitter and the
// WRITER it used.
//
// It replicates rather than calls streamMergeToFile because both the emitter and
// the writer are internal to that function, and a test about the writer's
// windows has to hold the writer. This is the package's one copy of that
// sequence: the structural gate's realMergeEmitterState is a thin wrapper over
// it, so the two cannot drift apart from streamMergeToFile independently.
func realMergeMachinery(
	t *testing.T,
	ins []*mappedSegment,
	accept []func(searchengine.ExternalID) bool,
	kind byte,
	sink searchengine.MergeSink,
) (*mergeEmitter, *mergeWriter) {
	t.Helper()
	members, remap := resolveMergeLayout(ins, accept)

	termCount := make([]int, len(defaultFieldConfigs))
	dfCount := 0
	mergeWalk(ins, remap,
		func(_ string, field int, _ []uint32, _ []uint16) { termCount[field]++ },
		func(string, int64) { dfCount++ })

	p := planMerge(kind, members, termCount, dfCount)
	w := newMergeWriter(sink, int64(p.prefixEnd))
	writeMergePrefix(w, p, ins, remap)
	e := newMergeEmitter(w, p)
	mergeWalk(ins, remap, e.field, e.term)
	e.flushBlocks()
	require.NoError(t, w.err)
	return e, w
}

// openOneLiveRun stores 16 bytes at offset 0, which opens exactly one live run
// and reaches the sink not at all, and asserts both of those facts.
//
// IT IS THE PRECONDITION EVERY TEST BELOW RESTS ON. Without the live-run count
// the "contiguous" stores would be contiguous with nothing, and without the
// empty-sink check a write attributed to the subject store might have been
// issued by this one.
func openOneLiveRun(t *testing.T, w *mergeWriter, sink *armOrderSink) {
	t.Helper()
	w.store(0, make([]byte, 16))

	live := 0
	for i := range w.runs {
		if w.runs[i].live {
			live++
		}
	}
	require.Equal(t, 1, live,
		"the opening store left %d live runs, so the store under test is not the contiguous case these tests are about", live)
	require.Equal(t, int64(0), w.runs[0].start, "the opening store did not open run 0")
	require.Len(t, w.runs[0].buf, 16, "the opening store did not buffer, so there is no live run to be contiguous with")
	require.Empty(t, sink.writes,
		"the opening store already reached the sink, so a write attributed to the subject store below would not be its own")
}

// TestContiguousOversizeStoreLeavesEveryWindowOnTheBackingArray is R1 at the
// changed function: the single-backing-array invariant survives a store that is
// both larger than a window and contiguous with a live one.
func TestContiguousOversizeStoreLeavesEveryWindowOnTheBackingArray(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)

	// CONTROL, WITHOUT WHICH THE POST-ASSERTION IS VACUOUS: every window starts at
	// cap mergeRunBytes, so "still at cap" afterwards says something.
	requireEveryWindowOnTheBackingArray(t, w, "at construction")
	openOneLiveRun(t, w, sink)
	requireEveryWindowOnTheBackingArray(t, w, "before the oversize store")

	// THE SUBJECT: larger than a window, starting exactly where the live run ends.
	w.store(16, make([]byte, 2*mergeRunBytes))

	requireEveryWindowOnTheBackingArray(t, w, "after the oversize store")
}

// TestContiguousOversizeStoreIsOnePassThroughWriteAndLeavesTheRunAlone is R3's
// forward direction, observed through a sink that records each write's offset
// and length.
func TestContiguousOversizeStoreIsOnePassThroughWriteAndLeavesTheRunAlone(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)
	openOneLiveRun(t, w, sink)

	startBefore, lenBefore, capBefore := w.runs[0].start, len(w.runs[0].buf), cap(w.runs[0].buf)
	require.Equal(t, mergeRunBytes, capBefore, "the live run is not on the backing array, so the comparison below says nothing")

	big := make([]byte, 2*mergeRunBytes)
	w.store(16, big)

	require.Len(t, sink.writes, 1,
		"a contiguous oversize store must reach the sink as exactly one pass-through write, got %v", sink.writes)
	require.Equal(t, sinkWrite{off: 16, n: len(big)}, sink.writes[0],
		"the pass-through write must carry the store's own offset and its own length")

	// LEFT UNTOUCHED, NOT FLUSHED. flushOverlapping tests STRICT overlap, so a run
	// ending exactly at the store's offset is deliberately left alone: flushing it
	// would cost the second sink write the assertion above forbids, and would
	// discard the per-stream slot affinity mergeRun's comment says the slots exist
	// to provide.
	require.Equal(t, startBefore, w.runs[0].start, "the contiguous run's start moved, so the run was flushed")
	require.Len(t, w.runs[0].buf, lenBefore, "the contiguous run's buffered bytes moved, so it was not left untouched")
	require.Equal(t, capBefore, cap(w.runs[0].buf), "the contiguous run's capacity moved, so it left the backing array")
}

// TestOversizeStoreThatFitsInsideAnOpenRunIsStillAnInPlaceOverwrite pins the
// boundary the arm PLACEMENT rests on, as opposed to the arm order.
//
// THE BAND IS 8,185..8,191 BYTES and it is narrow on purpose. The oversize
// predicate takes anything over mergeRunBytes-mergeRunMaxGap = 8,184; a live run
// holds at most mergeRunBytes-1 = 8,191 buffered bytes between store calls,
// because it flushes as soon as it reaches mergeRunBytes. A store inside that
// band is therefore large enough to trip the oversize predicate and still small
// enough to sit wholly inside an open window — the one input class for which
// "oversize arm second" and "oversize arm first of all four" disagree.
//
// WHAT IT PINS: the in-range arm stays FIRST, so such a store remains the
// in-place memory overwrite the routing comment says that arm exists to
// guarantee, rather than becoming a pass-through write. It is green both before
// and after the arm-order fix, because the in-range arm is first in both; it
// goes RED if the oversize arm is moved ahead of the in-range arm, which is the
// HNSW sibling's ordering and the alternative this implementation declined.
func TestOversizeStoreThatFitsInsideAnOpenRunIsStillAnInPlaceOverwrite(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)

	// FILL A RUN TO 8,191 BYTES IN TWO STORES. One 8,191-byte store could not do
	// it: 8,191 is itself over the oversize threshold, so it would never buffer.
	w.store(0, make([]byte, 8000))
	w.store(8000, make([]byte, 191))
	require.Len(t, w.runs[0].buf, mergeRunBytes-1,
		"the run did not reach %d buffered bytes, so no store in the band can fit inside it", mergeRunBytes-1)
	require.Empty(t, sink.writes, "filling the run already reached the sink, so the subject store's write count is not its own")
	require.Equal(t, mergeRunBytes, cap(w.runs[0].buf))

	// THE SUBJECT: over the oversize threshold, yet wholly inside the open run.
	payload := make([]byte, mergeRunBytes-mergeRunMaxGap+1)
	require.Greater(t, len(payload)+mergeRunMaxGap, mergeRunBytes,
		"the payload does not trip the oversize predicate, so this is not the band under test")
	require.LessOrEqual(t, len(payload), len(w.runs[0].buf),
		"the payload does not fit inside the open run, so this is not the band under test")
	for i := range payload {
		payload[i] = byte(i%251) + 1
	}
	w.store(0, payload)

	require.Empty(t, sink.writes,
		"a store that fits wholly inside an open run must stay a memory write, not reach the sink; got %v", sink.writes)
	require.Len(t, w.runs[0].buf, mergeRunBytes-1, "the in-place overwrite must not change how much the run holds")
	require.Equal(t, mergeRunBytes, cap(w.runs[0].buf), "the in-place overwrite must not move the run off the backing array")
	require.Equal(t, payload, w.runs[0].buf[:len(payload)],
		"the bytes did not land in the run, so the overwrite was a no-op rather than an in-place write")
}

// TestOverlappingOversizeStoreFlushesTheRunItOverlaps is R3's other direction,
// so "left untouched" cannot be read as "never flushes".
//
// IT IS A CONTROL, NOT A RED-BEFORE-GREEN WITNESS, and saying so is cheaper than
// letting a reader assume otherwise: the pass-through arm calls flushOverlapping
// under either arm ordering, so this holds on the tree before the order was
// fixed as well as after. What it rules out is a writer that satisfied the test
// above by not flushing anything at all.
func TestOverlappingOversizeStoreFlushesTheRunItOverlaps(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)
	openOneLiveRun(t, w, sink)

	// An oversize store starting INSIDE the live run's buffered range overlaps it,
	// where the store at offset 16 above merely abuts it.
	big := make([]byte, 2*mergeRunBytes)
	w.store(8, big)

	require.Len(t, sink.writes, 2,
		"the overlapped run must be flushed before the pass-through write, got %v", sink.writes)
	require.Equal(t, sinkWrite{off: 0, n: 16}, sink.writes[0], "the overlapped run is flushed first, at its own start")
	require.Equal(t, sinkWrite{off: 8, n: len(big)}, sink.writes[1], "then the store passes through at its own offset")
	require.Empty(t, w.runs[0].buf, "the overlapped run must be empty after its flush")
	require.Equal(t, mergeRunBytes, cap(w.runs[0].buf), "flushing must not move a run off the backing array")
}

// TestProductionShapeMergeKeepsEveryWindowOnTheBackingArray is amended R4's new
// bound: a merge whose stores really do reach the contiguous-oversize arm, run
// through the genuine emission path rather than a hand-built store sequence.
//
// THE FIXTURE IS SIZED FROM THE PREDICATE, not picked round. The oversize arm
// takes a store when len(b) > mergeRunBytes-mergeRunMaxGap = 8,184 bytes. A
// merged posting run is 6 bytes per document carrying the term and a merged
// per-field document-length array is 4 bytes per document, so 2,048 documents
// sharing a common term produce a 12,288-byte posting run and an 8,192-byte
// length array — both over the threshold. Posting runs are appended at the
// writer's own ascending tail, so the tail window's end IS the store's offset
// and the contiguity is systematic rather than incidental.
//
// THE RED IS THE VACUITY CONTROL. On a tree whose arms are in the old order this
// assertion can only pass if the fixture never reached the arm, so its failure
// there is what proves the fixture reaches it.
func TestProductionShapeMergeKeepsEveryWindowOnTheBackingArray(t *testing.T) {
	const corpus = 2048
	ins := mergeInputs(t, fieldDocs(corpus), 4)

	sink := newArmOrderSink(t)
	_, w := realMergeMachinery(t, ins, acceptEvery(len(ins)), defaultDictKind, sink)
	w.patchU32(v2HdrBlobLen, uint32(w.tail))
	w.flushAll()
	require.NoError(t, w.err)

	// THE MERGE DID REACH THE ARM: at least one block larger than a whole window
	// went to the sink. Without this the capacity assertion below would pass over
	// a merge that never produced an oversize store at all.
	oversize := 0
	for _, wr := range sink.writes {
		if wr.n > mergeRunBytes {
			oversize++
		}
	}
	require.Positive(t, w.tail, "the merge wrote nothing, so its windows describe nothing")
	require.Positive(t, oversize,
		"the fixture produced no write larger than a %d-byte window, so it never reached the oversize arm and the assertion below is vacuous",
		mergeRunBytes)
	t.Logf("production-shape merge over %d documents: %d sink writes, %d of them larger than a window, %d-byte segment",
		corpus, len(sink.writes), oversize, w.tail)

	requireEveryWindowOnTheBackingArray(t, w, "after a production-shape merge")
}

// 8,184 is the LAST length the oversize predicate must NOT take:
// len(b)+mergeRunMaxGap == mergeRunBytes exactly, and the predicate is strict.
// It must reach the EXTEND arm, which flushes the live run to make room and then
// buffers the payload — one sink write carrying the RUN's coordinates, not the
// store's.
func TestLastNonOversizeStoreTakesTheExtendArm(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)
	openOneLiveRun(t, w, sink)

	payload := make([]byte, mergeRunBytes-mergeRunMaxGap)
	require.Equal(t, mergeRunBytes, len(payload)+mergeRunMaxGap,
		"this payload is not on the predicate's boundary, so the test is not about the boundary")
	w.store(16, payload)

	require.Len(t, sink.writes, 1, "expected exactly the extend arm's overflow flush, got %v", sink.writes)
	require.Equal(t, sinkWrite{off: 0, n: 16}, sink.writes[0],
		"the one write must be the FLUSHED RUN's coordinates; a write at the store's own offset means the oversize arm took it")
	require.Len(t, w.runs[0].buf, len(payload),
		"the extend arm must have buffered the payload; a run still holding 16 bytes means the store was passed through")
	require.Equal(t, mergeRunBytes, cap(w.runs[0].buf))
}

// TestOversizeStoreThatAbutsOneRunAndOverlapsAnotherFlushesTheOverlappedRunFirst
// pins the one input class where the arm order changes the BYTES the sink ends
// up holding, not only the allocation: an oversize store that abuts one live
// run while overlapping another. Under the old order the extend arm absorbed it
// and the overlapped run's stale buffer was flushed LAST, over the store's
// bytes; with the pass-through arm first, flushOverlapping empties the
// overlapped run before the store reaches the sink. The emitter never produces
// the class (measured: 0 of 131 oversize stores over 42 merges abut one run
// while overlapping another), so this pins the writer's contract rather than a
// production path.
func TestOversizeStoreThatAbutsOneRunAndOverlapsAnotherFlushesTheOverlappedRunFirst(t *testing.T) {
	sink := newArmOrderSink(t)
	w := newMergeWriter(sink, 0)
	w.store(0, bytes.Repeat([]byte{0x01}, 16))
	w.store(8000, bytes.Repeat([]byte{0xaa}, 16))

	live := 0
	for i := range w.runs {
		if w.runs[i].live {
			live++
		}
	}
	require.Equal(t, 2, live,
		"the two opening stores must leave two live runs, or the store under test abuts and overlaps nothing")
	require.Empty(t, sink.writes,
		"the opening stores already reached the sink, so a write attributed to the subject store below would not be its own")

	w.store(16, bytes.Repeat([]byte{0x55}, 16384))
	w.flushAll()

	got := make([]byte, 1)
	_, err := sink.ReadAt(got, 8000)
	require.NoError(t, err)
	require.Equal(t, byte(0x55), got[0],
		"the overlapped run's stale bytes were flushed over the store's bytes: the pass-through arm must empty the run it overlaps before writing through")
}
