// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// serial_writer_reference_test.go is the writer's differential gate: for any
// sequence of stores, the destination must end up holding exactly what a plain
// offset-addressed destination would hold.
//
// IT IS A CENSUS RATHER THAN A CASE LIST because the failures this writer can
// have are not enumerable by inspection. Two rounds of review found ordering
// holes that every hand-written case missed, each time by a route nobody had
// thought to name: the first through a gap padded over bytes another window was
// holding, the second through a gap padded over bytes an earlier flush had
// already put in the sink. A random comparison against a reference does not need
// the route to be named in advance.
//
// THE REFERENCE IS io.WriterAt's OWN MEANING, which is what MergeSink is: a
// store at an offset places those bytes at that offset. Anything the coalescing
// writer does — holding, padding, flushing in some order — is an implementation
// of that contract, so the contract is the expectation and no part of it is
// taken from the writer under test.

// referenceStores applies ops to a plain byte slice: the meaning of the
// sequence, with no windows and no scheduling.
func referenceStores(size int, ops []storeOp) []byte {
	ref := make([]byte, size)
	for _, op := range ops {
		copy(ref[op.off:], op.b)
	}
	return ref
}

// randomStoreSequence draws a sequence of stores at random offsets and widths.
//
// THE SEQUENCES ARE UNCONSTRAINED ON PURPOSE. Restricting them to the shapes the
// encoder emits would make the census a restatement of the encoder rather than a
// statement about the writer, and it is the writer that is the reusable unit:
// the next emitter to use it will not have the current one's store order.
//
// UNCONSTRAINED IS NOT THE SAME AS UNIFORM, and the difference is what this
// generator got wrong for three review rounds. Drawing every width from one to
// twenty-four bytes over four kilobytes of address space cannot produce a store
// wider than a window, cannot fill one, and lands two stores adjacent only by
// coincidence — so the census ran with the pass-through, flush-before-overflow
// and pass-hull arms at zero and certified a writer it had never asked those
// questions of. The classes below are stated in terms of the SINK CONTRACT and
// not of the writer: a caller may store more bytes than any buffer could hold,
// and an emitter that advances cursors stores adjacently as its ordinary case.
// The per-arm reach assertions in the census are what keep this honest, since a
// generator can lose an arm as silently as it never had it.
func randomStoreSequence(rng *rand.Rand) (ops []storeOp, size int) {
	// A few kilobytes keeps small stores colliding constantly, which is what puts
	// two windows in conflict at all; one sequence in four spans several windows
	// instead, so eviction and coverage are also exercised at window scale.
	space := 64 + rng.IntN(4096)
	if rng.IntN(4) == 0 {
		space = v3WriteRunBytes + rng.IntN(3*v3WriteRunBytes)
	}
	n := 1 + rng.IntN(60)
	ops = make([]storeOp, 0, n)
	for range n {
		width := 1 + rng.IntN(24)
		switch {
		case rng.IntN(192) == 0:
			// Wider than a window can hold beside a maximal gap. The contract admits
			// it, so the writer has to have somewhere to put it.
			//
			// RARE, AND ONLY JUST OVERSIZE, because both knobs are pure cost and
			// neither buys reach. Every store of this class is filled, copied into
			// the reference, copied back out of the sink and compared, so the
			// census's wall time runs with the bytes this class draws — and a store
			// one byte past the threshold takes the arm exactly as a store at twice
			// the threshold does.
			width = v3WriteRunBytes - v3WriteRunMaxGap + 1 + rng.IntN(v3WriteRunBytes/8)
		case rng.IntN(24) == 0:
			// Fits a window on its own but not one already holding bytes.
			width = v3WriteRunBytes - v3WriteRunMaxGap - rng.IntN(64)
		}
		off := rng.IntN(space)
		if len(ops) > 0 && rng.IntN(2) == 0 {
			// Adjacent to some earlier store's end, jittered either side of it: back
			// inside it, flush against it, across an absorbable gap, or just past one.
			prev := ops[rng.IntN(len(ops))]
			off = max(0, prev.off+len(prev.b)+rng.IntN(2*v3WriteRunMaxGap+2)-v3WriteRunMaxGap)
		}
		b := make([]byte, width)
		// A NON-ZERO RAMP rather than independently drawn bytes. Non-zero is what
		// makes a pad visible as a loss, since zero is the only thing padding
		// writes; the ramp additionally makes a MISPLACED copy visible, where a
		// constant fill would not be. It is one draw per store rather than one per
		// byte, which the wide classes above would otherwise make prohibitive.
		v := byte(1 + rng.IntN(255))
		for i := range b {
			b[i] = v
			v++
			if v == 0 {
				v = 1
			}
		}
		ops = append(ops, storeOp{off: off, b: b})
		size = max(size, off+width)
	}
	return ops, size
}

// addArms accumulates one writer's arm counts into a running total.
func addArms(dst *v3WriteArms, src v3WriteArms) {
	dst.oversize += src.oversize
	dst.inRange += src.inRange
	dst.extend += src.extend
	dst.evict += src.evict
	dst.flushOverflow += src.flushOverflow
	dst.declinedGap += src.declinedGap
	dst.declinedSlot += src.declinedSlot
	dst.declinedPass += src.declinedPass
}

// TestV3WriterMatchesAPlainWriterAtOverRandomSequences is THE differential gate.
//
// EVERY BYTE IS NON-ZERO IN THE FIXTURE, which is what makes a lost byte
// visible: the only way a zero appears in the destination is padding, so a zero
// where the reference has a value is precisely the failure this census exists to
// catch, rather than a coincidence of the data.
func TestV3WriterMatchesAPlainWriterAtOverRandomSequences(t *testing.T) {
	const sequences = 20000

	rng := rand.New(rand.NewPCG(0x5150, 0x8086))
	diverged, totalStores, totalWrites := 0, 0, 0
	var arms v3WriteArms
	var firstBad []storeOp
	var firstBadSize int

	for range sequences {
		ops, size := randomStoreSequence(rng)

		s := &recordingSink{}
		w := newV3Writer(s)
		for _, op := range ops {
			w.put(op.off, op.b)
		}
		w.flushAll()
		require.NoError(t, w.err)

		got := make([]byte, size)
		copy(got, s.buf)
		totalStores += len(ops)
		totalWrites += len(s.calls)
		addArms(&arms, w.arms)

		if string(got) != string(referenceStores(size, ops)) {
			diverged++
			if firstBad == nil {
				firstBad, firstBadSize = ops, size
			}
		}
	}

	t.Logf("%d sequences, %d stores, %d write calls (%.3f per store), %d diverged",
		sequences, totalStores, totalWrites, float64(totalWrites)/float64(totalStores), diverged)
	t.Logf("arm reach: oversize=%d inRange=%d extend=%d evict=%d flushOverflow=%d declinedGap=%d "+
		"declinedSlot=%d declinedPass=%d",
		arms.oversize, arms.inRange, arms.extend, arms.evict, arms.flushOverflow,
		arms.declinedGap, arms.declinedSlot, arms.declinedPass)

	// THE VERDICT IS REPORTED BEFORE THE CONTROLS ON THE INSTRUMENT, which is the
	// reverse of the usual order and is deliberate. The controls below exist to
	// catch a census that passed BECAUSE it exercised nothing, so they are only
	// load-bearing on a run that found no divergence; and a guard whose deletion
	// both loses bytes and zeroes the counter that observes it — which is every
	// coverage guard here — would otherwise report as "an arm was not reached"
	// when the news is that the writer lost bytes.
	if diverged > 0 {
		got, want := replayThroughBothWriters(t, firstBad)
		t.Fatalf("%d of %d random store sequences produced a destination that differs from a plain offset-addressed "+
			"one. The writer is not an implementation of the sink contract it is handed. First divergence, over %d "+
			"bytes:\n  got  %v\n  want %v", diverged, sequences, firstBadSize, got, want)
	}

	require.Greater(t, totalStores, sequences, "control: the census generated fewer stores than sequences")

	// THE CONTROL ON THE CENSUS ITSELF IS PER-ARM REACH, and it is per-arm
	// because the aggregate could not discriminate. The ratio of write calls to
	// stores was this control until it was measured: at 0.980 writes per store it
	// passed while the extend arm ran on 0.4% of stores and three arms ran on
	// none, since eviction alone — a call per store — satisfies a bound of "fewer
	// calls than stores" as soon as any two stores share one window.
	//
	// WHAT THESE COUNTS PROVE, exactly. Each one says the census executed that
	// arm at least once against a plain WriterAt reference, so a defect confined
	// to it had an opportunity to diverge and did not. They do NOT say the arm is
	// exhaustively covered, and no count here is a quality floor: the assertion is
	// reach, which is the property the census silently lost three times.
	for _, reach := range []struct {
		arm string
		n   int
	}{
		{"the oversize pass-through arm", arms.oversize},
		{"the in-range overwrite arm", arms.inRange},
		{"the contiguous-extend arm", arms.extend},
		{"the round-robin eviction arm", arms.evict},
		{"the extend arm's flush-before-overflow guard", arms.flushOverflow},
		{"an extend declined by another slot's coverage hull", arms.declinedSlot},
		{"an extend declined by the pass-through coverage hull", arms.declinedPass},
	} {
		require.Positive(t, reach.n,
			"control: over %d sequences and %d stores this census never reached %s, so it certifies nothing about "+
				"it — a guard on that arm could be deleted and this test would still pass. Widen randomStoreSequence "+
				"until it does rather than dropping the assertion.",
			sequences, totalStores, reach.arm)
	}
}

// TestV3WriterDoesNotPadOverBytesAlreadyInTheSink is the named case for the
// class the census found: a gap absorbed over a range an earlier flush already
// wrote.
//
// IT IS HERE AS WELL AS IN THE CENSUS because a census reports a count and this
// reports a mechanism. The three stores are the smallest sequence that reaches
// it: the first opens a window, the second forces that window to flush and takes
// a slot of its own, and the third extends the second across a gap that spans
// what the first already put in the destination.
func TestV3WriterDoesNotPadOverBytesAlreadyInTheSink(t *testing.T) {
	ops := []storeOp{
		{off: 96, b: []byte{26, 28, 148, 150, 180, 103, 161}},
		{off: 93, b: []byte{150, 237, 240, 250}},
		{off: 102, b: []byte{150}},
	}
	got, want := replayThroughBothWriters(t, ops)
	require.Equal(t, want, got,
		"the absorbed gap wrote zeros over bytes an earlier flush had already placed in the destination. A window "+
			"that has flushed still OWNS the range it wrote: its bytes are in the sink, where padding cannot see "+
			"them and does not ask.")
}

// TestV3WriterDoesNotPadOverBytesWrittenStraightThrough is the named case for the
// third member of that family: a gap absorbed over a range a store too large for
// any window already wrote.
//
// IT NEEDS A HULL OF ITS OWN BECAUSE THE BYTES BELONG TO NO SLOT. The other two
// classes conflict with a range some window covers, held or already flushed, so
// a per-slot record answers them. A store wider than a window never enters a
// window at all: it goes to the sink from the routing arm itself, and every
// slot's coverage says, correctly, that it was never there. Without a separate
// record the extend arm reads that range as unwritten and pads it.
//
// THE THREE STORES ARE THE SMALLEST SEQUENCE THAT REACHES IT: the first is
// passed through, the second opens a window ending four bytes below it, and the
// third sits six bytes past that window's end — inside the absorb — so the span
// the extend would newly cover runs four bytes into what the first store wrote.
//
// THE ASSERTION REPORTS THE FIRST DIVERGING BYTE rather than comparing the two
// three-hundred-kilobyte destinations with require.Equal, whose failure render
// would be the whole blob twice. The offset and the two values are what names
// the mechanism.
func TestV3WriterDoesNotPadOverBytesWrittenStraightThrough(t *testing.T) {
	ops := []storeOp{
		{off: 100000, b: filled(200000, 8)},
		{off: 99994, b: filled(4, 1)},
		{off: 100004, b: filled(4, 2)},
	}
	require.Greater(t, len(ops[0].b)+v3WriteRunMaxGap, v3WriteRunBytes,
		"control: the first store must be too wide for a window, or it never takes the pass-through arm and this "+
			"case exercises no hull at all")

	got, want := replayThroughBothWriters(t, ops)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("first mismatch at byte %d: got %d, want %d. The absorbed gap wrote zeros over bytes a "+
				"pass-through store had already placed in the destination. A store too wide for a window enters no "+
				"window, so no slot's coverage remembers the range it wrote and only its own hull can decline the "+
				"padding.", i, got[i], want[i])
		}
	}
}
