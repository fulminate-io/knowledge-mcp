// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// serial_writer_order_test.go covers the writer's ORDERING invariants: which
// window a store lands in, which windows are flushed before it lands, and what
// the destination therefore holds once every window has gone out.
//
// THEY ARE SEPARATED FROM THE ROUTING CLASSES because they are a different kind
// of assertion. serial_writer_test.go asks which arm a store takes; these ask
// whether the bytes survive the taking, which is the property the arms exist
// for and the one that fails silently when an arm is wrong.

// storeOp is one put in a replayed sequence.
type storeOp struct {
	off int
	b   []byte
}

// replayThroughBothWriters runs the same store sequence through the coalescing
// writer and through a plain offset-addressed reference, and returns both.
//
// THE REFERENCE IS THE EXTERNAL EXPECTATION. Comparing the writer's output
// against bytes transcribed from a previous run of the writer would ratify
// whatever it currently does; comparing it against a destination that simply
// copies each store where it was addressed says what the SEQUENCE means,
// independently of how any writer chooses to schedule it. It is the same
// discipline the write-count gate uses in taking its denominator from the
// fixture rather than from the writer.
func replayThroughBothWriters(t *testing.T, ops []storeOp) (buffered, reference []byte) {
	buffered, reference, _ = replayAndRecord(t, ops)
	return buffered, reference
}

// replayAndRecord is replayThroughBothWriters with the write calls kept, for the
// cases that assert which route the writer took.
func replayAndRecord(t *testing.T, ops []storeOp) (buffered, reference []byte, calls []sinkCall) {
	t.Helper()

	// THE SIZE IS DERIVED FROM THE STORES, never declared beside them. A
	// hand-written size smaller than the sequence reaches panics on the reference
	// copy below and reads as a failure of the writer under test, which is what
	// the first draft of this helper did.
	size := 0
	for _, op := range ops {
		size = max(size, op.off+len(op.b))
	}

	s := &recordingSink{}
	w := newV3Writer(s)
	for _, op := range ops {
		w.put(op.off, op.b)
	}
	w.flushAll()
	require.NoError(t, w.err)

	reference = make([]byte, size)
	for _, op := range ops {
		copy(reference[op.off:], op.b)
	}

	buffered = make([]byte, size)
	copy(buffered, s.buf)
	return buffered, reference, s.calls
}

// TestV3WriterHoldsBytesThroughEveryOrderingHazard drives the store sequences
// where two windows can end up addressing the same bytes, and asserts the
// destination holds what the stores said regardless of which window flushed
// first.
//
// EVERY CASE HERE IS A SEQUENCE THE ROUTING CAN PRODUCE, not a synthetic one.
// The writer holds bytes and releases them on its own schedule, so a store that
// lands in one window while another window still holds bytes in the same range
// makes the destination depend on the slot order the round-robin cursor happened
// to reach — a dependency no caller can see or control, and one that inverts
// silently when a slot count or an eviction order changes.
func TestV3WriterHoldsBytesThroughEveryOrderingHazard(t *testing.T) {
	// Four widely-spaced fillers occupy every slot and leave the eviction cursor
	// back at slot 0, so the store that follows them opens its window in a LOWER
	// slot than the window it conflicts with — which is the order in which a
	// missing flush loses bytes rather than getting away with it.
	const slotStride = v3WriteRunBytes * 4 // wider than any window, so no filler can absorb another
	lastSlot := (v3WriteRunSlots - 1) * slotStride
	fillSlots := func() []storeOp {
		ops := make([]storeOp, 0, v3WriteRunSlots)
		for i := range v3WriteRunSlots {
			ops = append(ops, storeOp{off: i * slotStride, b: filled(4, byte(0xF0+i))})
		}
		return ops
	}

	// wantCalls is what makes each case OBSERVABLE. A case named for a routing
	// class that quietly stopped driving it still compares equal to the
	// reference, because a writer that took a different and also-correct route
	// produces the same bytes. Pinning the write shape is what says which route
	// was taken. This is the same remedy the routing table uses with callsAtPut,
	// applied here after two of these cases were found to be named for an arm
	// they did not reach.
	for _, tc := range []struct {
		name      string
		ops       []storeOp
		wantCalls []sinkCall
	}{
		{
			// THE EXTEND ARM'S GAP CROSSING ANOTHER WINDOW'S HELD BYTES. The third
			// store extends the second window across an eight-byte absorbed gap, and
			// that gap covers bytes the FIRST window is still holding. The padding is
			// not data — it is a stand-in for a destination that reads as zeros — so
			// writing it over a window that holds real bytes destroys them.
			name: "an absorbed gap that crosses a live window's held bytes",
			ops: []storeOp{
				{off: 100, b: filled(6, 1)},
				{off: 90, b: filled(6, 2)},
				{off: 104, b: filled(4, 3)},
			},
			// The third store is DECLINED by the coverage check and opens its own
			// window, which is the three calls; absorbed, it would be two.
			wantCalls: []sinkCall{{100, 6}, {90, 6}, {104, 4}},
		},
		{
			// A BACKPATCH INTO A WINDOW THAT STILL HOLDS THE BYTES, which the
			// in-range arm absorbs as a memory write: two calls, not three.
			//
			// AN EARLIER NAME HERE CLAIMED IT ABSORBED A GAP. It never did — an arm
			// trace showed all three stores taking the new-window and in-range arms
			// and no gap being padded at all. The gap classes are the case above and
			// TestV3WriterDoesNotPadOverBytesAlreadyInTheSink.
			name: "a backpatch into a window that still holds the bytes",
			ops: []storeOp{
				{off: 40, b: filled(16, 1)},
				{off: 30, b: filled(4, 2)},
				{off: 40, b: filled(4, 3)},
			},
			wantCalls: []sinkCall{{40, 16}, {30, 4}},
		},
		{
			// THE EVICTION ARM'S OVERLAP. The last store partially overlaps a window
			// that is neither able to absorb it (it starts behind that window's end,
			// so the gap is negative) nor able to hold it (it runs past that window's
			// end, so it is not in range). It therefore opens a NEW window, in a
			// lower slot than the one it conflicts with. Without a flush of the
			// conflicting window first, that window goes out LAST and its older bytes
			// land on top.
			name: "a new window opening over a live window's held bytes, in a lower slot",
			ops: append(fillSlots(),
				storeOp{off: lastSlot + 4, b: filled(20, 5)},
				storeOp{off: lastSlot + 14, b: filled(20, 6)},
			),
			// THE ORDER IS THE ASSERTION, not the set. flushOverlapping writes the
			// conflicting window's 24 bytes at lastSlot FIRST; the eviction then
			// flushes whatever slot 0 held; the new window's 20 bytes at
			// lastSlot+14 land after. Reverse the first and third of those and the
			// older bytes win the overlap.
			wantCalls: []sinkCall{
				{int64(lastSlot), 24}, {0, 4}, {int64(lastSlot + 14), 20},
				{int64(slotStride), 4}, {int64(2 * slotStride), 4},
			},
		},
		{
			// A store landing exactly at a held window's START, the boundary of the
			// in-range arm's range test.
			//
			// ITS NAME USED TO CLAIM the in-range arm's POSITION ahead of the extend
			// arm is what catches it. That claim is gone from the writer too: the
			// coverage check took the guarantee over, and the extend arm's gap test
			// is negative for a store inside a window, so the order cannot decide the
			// bytes. What the order still buys is a write call, which is what this
			// case pins.
			name: "a store landing exactly at a held window's start",
			ops: []storeOp{
				{off: 20, b: filled(12, 1)},
				{off: 10, b: filled(10, 2)},
				{off: 20, b: filled(4, 3)},
			},
			wantCalls: []sinkCall{{20, 12}, {10, 10}},
		},
		{
			// THE EXTEND ARM'S GAP CROSSING BYTES A PASS-THROUGH STORE ALREADY
			// PLACED. The first store is too wide for any window, so it goes straight
			// to the sink and belongs to no slot: no coverage hull on any run
			// remembers it. The second opens a window just below it. The third sits
			// six bytes past that window's held end, inside the absorb, and the span
			// it would newly cover runs over four bytes the first store already
			// placed. Padding is a stand-in for a destination that reads as zeros,
			// which those four bytes are not.
			//
			// IT IS THE ONE CLASS THE OTHER HAZARDS CANNOT REACH, because every one
			// of them conflicts with a range some SLOT covers. A store larger than a
			// window never enters a slot at all, so the writer needs a hull of its
			// own for it and this case is what says the hull is consulted.
			name: "an absorbed gap that crosses bytes written straight through",
			ops: []storeOp{
				{off: 100000, b: filled(200000, 8)},
				{off: 99994, b: filled(4, 1)},
				{off: 100004, b: filled(4, 2)},
			},
			// The third store is DECLINED by the pass-through hull and opens its own
			// window, which is the three calls. Absorbed, it would be two — the
			// pass-through and one 14-byte flush at 99994 whose six pad bytes cover
			// [99998,100004), four of them over the pass-through's own.
			wantCalls: []sinkCall{{100000, 200000}, {99994, 4}, {100004, 4}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buffered, reference, calls := replayAndRecord(t, tc.ops)
			s := struct{ calls []sinkCall }{calls}

			// The control on the fixture: a sequence whose stores do not overlap
			// would satisfy this comparison however the windows behaved.
			covered := make([]bool, len(reference))
			overlapping := false
			for _, op := range tc.ops {
				for i := op.off; i < op.off+len(op.b); i++ {
					if covered[i] {
						overlapping = true
					}
					covered[i] = true
				}
			}
			require.True(t, overlapping,
				"control: this case's stores address disjoint ranges, so it exercises no ordering hazard at all")

			require.Equal(t, tc.wantCalls, s.calls,
				"this case no longer drives the routing it is named for: it issued %v where the class produces %v",
				s.calls, tc.wantCalls)

			require.Equal(t, reference, buffered,
				"the destination does not hold what the stores said. The coalescing writer let two windows address "+
					"the same bytes and the slot order decided which won, which is a dependency no caller can see: "+
					"the same stores through a plain WriterAt produce the reference above.")
		})
	}
}

// TestV3WriterFlushesAWindowBeforeItWouldOverflow drives put's flush-before-
// overflow guard, which is the one routing arm no other test reaches.
//
// WHY NOTHING ELSE REACHES IT. The guard fires when a store both FITS a window
// and would push a partially-filled window past its capacity. The encoder's own
// stores are four bytes wide and fill windows to their bound exactly, so they
// leave through the trailing full-window check instead; and a store large enough
// to overflow a fresh window is diverted by the oversize arm before the extend
// arm can see it. Two stores of forty thousand bytes are the shape that lands in
// between: each fits a window alone, and together they do not.
//
// WHAT BREAKS WITHOUT IT. Each window is a slice into one pre-sized backing
// array with capacity of exactly one window, so appending past that capacity
// makes append allocate a fresh buffer — the per-store heap growth the fixed
// window exists to prevent, and the shape that produced the sibling format's
// landed defect.
func TestV3WriterFlushesAWindowBeforeItWouldOverflow(t *testing.T) {
	const half = 40000
	require.Less(t, half+v3WriteRunMaxGap, v3WriteRunBytes,
		"control: each store must FIT a window on its own, or it takes the oversize arm and never reaches the guard")
	require.Greater(t, half*2, v3WriteRunBytes,
		"control: the two stores together must EXCEED a window, or nothing would overflow")

	s := &recordingSink{}
	w := newV3Writer(s)
	w.put(0, filled(half, 0xA1))
	w.put(half, filled(half, 0xB2))
	w.flushAll()
	require.NoError(t, w.err)

	require.Equal(t, []sinkCall{{0, half}, {half, half}}, s.calls,
		"the writer issued %v. Two calls, split at the first store's end, is the window being flushed BEFORE the "+
			"second store is appended. One call of %d bytes is append having grown the window past its capacity, "+
			"which allocates a fresh buffer outside the backing array.", s.calls, half*2)
	require.Equal(t, append(filled(half, 0xA1), filled(half, 0xB2)...), s.buf)
}

// TestV3WriterKeepsEachStreamOnItsOwnWindow is the state-transition gate: a run
// must stay LIVE across its own flushes, with start advanced and buf emptied.
//
// IT IS THE PROPERTY THE WHOLE MECHANISM RESTS ON. If a flush closed its run,
// the next store in that stream would fall through to round-robin eviction and
// could land on a different slot every window — which is per-stream affinity
// lost, and the write count regresses toward one call per store. Four streams is
// the encoder's own shape: the node directory, the id bytes, the layer offsets
// and the neighbor arena advance together.
func TestV3WriterKeepsEachStreamOnItsOwnWindow(t *testing.T) {
	const (
		streams     = v3WriteRunSlots
		windowsEach = 2
		chunk       = 4096
		perStream   = v3WriteRunBytes * windowsEach
	)
	// The streams are placed a whole stream apart so nothing can be absorbed
	// across them: any coalescing observed is within a stream, by design.
	base := func(i int) int { return i * perStream * 2 }

	s := &recordingSink{}
	w := newV3Writer(s)
	for off := 0; off < perStream; off += chunk {
		for i := range streams {
			w.put(base(i)+off, filled(chunk, byte(0x10+i)))
		}
	}
	w.flushAll()
	require.NoError(t, w.err)

	require.Len(t, s.calls, streams*windowsEach,
		"the writer issued %d calls for %d streams of %d whole windows each. Exactly one call per filled window "+
			"means every stream stayed on its own slot; more means a run closed on flush and the streams are "+
			"evicting each other.", len(s.calls), streams, windowsEach)
	for _, c := range s.calls {
		require.Equal(t, v3WriteRunBytes, c.n, "a call carried %d bytes, so a window went out partly filled", c.n)
	}
	for i := range streams {
		want := filled(perStream, byte(0x10+i))
		require.Equal(t, want, s.buf[base(i):base(i)+perStream],
			"stream %d's bytes are wrong, so a window carried another stream's content", i)
	}
}

// TestV3WriterEvictionNeverLeavesTwoWindowsOverlapping drives more live streams
// than there are slots, which is the only way eviction runs, and asserts that no
// two writes ever touch the same byte.
//
// THE DISJOINTNESS IS THE ASSERTION, AND IT IS NARROWER THAN IT READS. It says
// this SEQUENCE never writes a byte twice; it does not say the writer cannot.
// The sequences that CAN are in TestV3WriterHoldsBytesThroughEveryOrderingHazard
// above, which asserts the stronger thing — that the destination holds what the
// stores said — because a byte written twice is only a defect when the second
// write is the wrong one.
func TestV3WriterEvictionNeverLeavesTwoWindowsOverlapping(t *testing.T) {
	const streams = v3WriteRunSlots + 2
	const stores = 8
	const chunk = 16
	base := func(i int) int { return 1 + i*v3WriteRunBytes }

	s := &recordingSink{}
	w := newV3Writer(s)
	for j := range stores {
		for i := range streams {
			w.put(base(i)+j*chunk, filled(chunk, byte(0x20+i)))
		}
	}
	w.flushAll()
	require.NoError(t, w.err)
	require.Greater(t, len(s.calls), streams,
		"control: %d streams over %d slots must have forced eviction, so more calls than streams", streams, v3WriteRunSlots)

	for a := range s.calls {
		for b := a + 1; b < len(s.calls); b++ {
			x, y := s.calls[a], s.calls[b]
			require.Falsef(t, x.off < y.off+int64(y.n) && y.off < x.off+int64(x.n),
				"write %d [%d,%d) and write %d [%d,%d) overlap — two windows held the same bytes and the "+
					"output depends on which flushed last",
				a, x.off, x.off+int64(x.n), b, y.off, y.off+int64(y.n))
		}
	}
	for i := range streams {
		want := filled(stores*chunk, byte(0x20+i))
		require.Equal(t, want, s.buf[base(i):base(i)+stores*chunk], "stream %d's bytes are wrong after eviction", i)
	}
}
