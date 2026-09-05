// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// serial_writer_test.go covers the WRITER's own arms: every routing class put
// can take, the two state transitions the coalescing depends on, and the error
// arms — including the flush barriers, which are the reason a buffered writer
// can turn a write failure into a corrupt-looking success if it gets them wrong.
//
// THE COUNT GATES LIVE NEXT DOOR, in serial_writecount_test.go. They say the
// writer coalesces; this file says WHAT IT WRITES while doing so.

// sinkCall is one WriteAt the writer issued.
type sinkCall struct {
	off int64
	n   int
}

// recordingSink is a MergeSink that records every write and keeps the bytes, so
// a test can assert both the call SHAPE and the resulting CONTENT.
//
// IT GROWS TO FIT, which is what makes the length assertions meaningful: the
// recorded buffer's length is the encode's high-water mark, so a writer that
// padded past the blob's end would be caught by comparing it to the length the
// emitter reported rather than by trusting that it did not.
type recordingSink struct {
	buf   []byte
	calls []sinkCall
	// failWrite, when set, decides whether a write fails. It runs BEFORE the
	// bytes land, so a refused write leaves the sink exactly as it was.
	failWrite func(off int64, n int) error
	// attempts counts every WriteAt the writer entered, refused or not. A refused
	// write leaves no other trace, and "no second write was ATTEMPTED after the
	// first failed" is exactly the property the held error exists to provide.
	attempts int
	reads    int
}

func (s *recordingSink) WriteAt(p []byte, off int64) (int, error) {
	s.attempts++
	if s.failWrite != nil {
		if err := s.failWrite(off, len(p)); err != nil {
			return 0, err
		}
	}
	s.calls = append(s.calls, sinkCall{off: off, n: len(p)})
	if end := int(off) + len(p); end > len(s.buf) {
		s.buf = append(s.buf, make([]byte, end-len(s.buf))...)
	}
	copy(s.buf[off:], p)
	return len(p), nil
}

func (s *recordingSink) ReadAt(p []byte, off int64) (int, error) {
	s.reads++
	if off < 0 || off > int64(len(s.buf)) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func filled(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

// TestV3WriterRoutesEveryStoreClass walks put's arms one class at a time. Each
// case states the calls the writer is allowed to issue and the bytes the sink
// must end up holding, so a routing change that preserved the count while
// moving the bytes is caught as surely as one that moved the count.
func TestV3WriterRoutesEveryStoreClass(t *testing.T) {
	// callsAtPut is what makes the arm OBSERVABLE rather than inferred: a buffered
	// store reaches the sink only at flushAll, a passed-through one reaches it
	// immediately, and both end with the same total. Without this the two are
	// indistinguishable at the end of the test, which is how a case came to be
	// named for an arm it never took.
	for _, tc := range []struct {
		name       string
		store      func(w *v3Writer)
		callsAtPut int
		calls      []sinkCall
		want       []byte
	}{
		{
			// A backpatch of a value the same window already carries stays a
			// memory write and never reaches the sink twice.
			name: "in-range overwrite is a memory write",
			store: func(w *v3Writer) {
				w.put(0, []byte{1, 2, 3, 4})
				w.put(0, []byte{9, 9})
			},
			calls: []sinkCall{{0, 4}},
			want:  []byte{9, 9, 3, 4},
		},
		{
			name: "a contiguous extend stays one window",
			store: func(w *v3Writer) {
				w.put(0, []byte{1, 2, 3, 4})
				w.put(4, []byte{5, 6, 7, 8})
			},
			calls: []sinkCall{{0, 8}},
			want:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
		{
			// The widest gap the format can open — v3EntReserved's four bytes and
			// an alignment's three are both well inside it.
			name: "a gap of exactly v3WriteRunMaxGap is absorbed as zero padding",
			store: func(w *v3Writer) {
				w.put(0, []byte{1, 2, 3, 4})
				w.put(4+v3WriteRunMaxGap, []byte{5, 6, 7, 8})
			},
			calls: []sinkCall{{0, 4 + v3WriteRunMaxGap + 4}},
			want:  []byte{1, 2, 3, 4, 0, 0, 0, 0, 0, 0, 0, 0, 5, 6, 7, 8},
		},
		{
			// One byte past the absorb is a new stream, not a padded one: absorbing
			// an unbounded gap would write megabytes of zeros to skip a section.
			name: "a gap one byte wider than the absorb opens a new window",
			store: func(w *v3Writer) {
				w.put(0, []byte{1, 2, 3, 4})
				w.put(4+v3WriteRunMaxGap+1, []byte{5, 6, 7, 8})
			},
			calls: []sinkCall{{0, 4}, {4 + v3WriteRunMaxGap + 1, 4}},
			want:  append(append([]byte{1, 2, 3, 4}, make([]byte, v3WriteRunMaxGap+1)...), 5, 6, 7, 8),
		},
		{
			// THE BOUNDARY, UPPER SIDE. A store two bytes short of a whole window
			// still does not FIT one alongside a maximal gap, so it takes the oversize
			// arm and reaches the sink at put time; the small store after it then
			// opens a window of its own. Paired with the case below, which is one
			// store smaller and is buffered, this brackets the arm-(1) boundary.
			//
			// IT IS NOT THE FLUSH-BEFORE-OVERFLOW GUARD, though an earlier name here
			// claimed it was. That guard lives in the extend arm and is driven by
			// TestV3WriterFlushesAWindowBeforeItWouldOverflow, which needs two stores
			// that each fit a window and together do not.
			name: "a store two bytes short of a window still passes through",
			store: func(w *v3Writer) {
				w.put(0, filled(v3WriteRunBytes-2, 0xAA))
				w.put(v3WriteRunBytes-2, filled(4, 0xBB))
			},
			callsAtPut: 1,
			calls:      []sinkCall{{0, v3WriteRunBytes - 2}, {v3WriteRunBytes - 2, 4}},
			want:       append(filled(v3WriteRunBytes-2, 0xAA), filled(4, 0xBB)...),
		},
		{
			// The largest store that still fits a window alongside a maximal gap.
			// One byte more is the pass-through case below.
			name: "the largest buffered store is a window less a maximal gap",
			store: func(w *v3Writer) {
				w.put(0, filled(v3WriteRunBytes-v3WriteRunMaxGap, 0xCC))
			},
			calls: []sinkCall{{0, v3WriteRunBytes - v3WriteRunMaxGap}},
			want:  filled(v3WriteRunBytes-v3WriteRunMaxGap, 0xCC),
		},
		{
			// THE VECTOR BLOCK'S CLASS. It is one store of nodeCount*vecBytes bytes
			// — megabytes on a production graph — and it must cost ONE write call
			// and never be split, whichever side of the window bound it lands on.
			name:       "a store larger than a window passes straight through in one call",
			callsAtPut: 1,
			store: func(w *v3Writer) {
				w.put(0, filled(v3WriteRunBytes*3, 0xDD))
			},
			calls: []sinkCall{{0, v3WriteRunBytes * 3}},
			want:  filled(v3WriteRunBytes*3, 0xDD),
		},
		{
			// A pass-through must not race a window still holding bytes in its
			// range, so the overlapping window is flushed first and in order.
			name:       "a pass-through flushes an overlapping window first",
			callsAtPut: 2,
			store: func(w *v3Writer) {
				w.put(0, []byte{1, 2, 3, 4})
				w.put(0, filled(v3WriteRunBytes*2, 0xEE))
			},
			calls: []sinkCall{{0, 4}, {0, v3WriteRunBytes * 2}},
			want:  filled(v3WriteRunBytes*2, 0xEE),
		},
		{
			name:  "a zero-length store is a no-op",
			store: func(w *v3Writer) { w.put(0, nil); w.put(4, []byte{}) },
			calls: nil,
			want:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &recordingSink{}
			w := newV3Writer(s)
			tc.store(w)
			require.Len(t, s.calls, tc.callsAtPut,
				"before the flush the sink had seen %v; a store that reached it here was passed straight through, "+
					"one that did not was buffered", s.calls)
			w.flushAll()
			require.NoError(t, w.err)
			require.Equal(t, tc.calls, s.calls, "the writer issued a different sequence of writes than the class allows")
			require.Equal(t, tc.want, s.buf, "the bytes the sink ended up holding are not the bytes the stores described")
		})
	}
}

// TestV3WriterFlushAllIsIdempotentAndNeverWritesNothing covers flushAll's three
// states, which matter because encodeGraphV3To calls it at both barriers and a
// second call must not re-issue the first one's bytes.
func TestV3WriterFlushAllIsIdempotentAndNeverWritesNothing(t *testing.T) {
	t.Run("with no bytes held it issues nothing", func(t *testing.T) {
		s := &recordingSink{}
		w := newV3Writer(s)
		w.flushAll()
		require.Empty(t, s.calls, "flushAll wrote something with every window empty")
	})

	t.Run("with bytes held it issues them once, and again issues nothing", func(t *testing.T) {
		s := &recordingSink{}
		w := newV3Writer(s)
		w.put(0, []byte{1, 2, 3, 4})
		w.flushAll()
		require.Equal(t, []sinkCall{{0, 4}}, s.calls)

		w.flushAll()
		require.Equal(t, []sinkCall{{0, 4}}, s.calls,
			"the second flushAll re-issued bytes the first one already wrote — at the footer barrier that would "+
				"rewrite the whole blob after the checksum covered it")

		// The window stayed LIVE with start advanced, so the stream continues on
		// its own slot rather than falling through to eviction.
		w.put(4, []byte{5, 6})
		w.flushAll()
		require.Equal(t, []sinkCall{{0, 4}, {4, 2}}, s.calls)
		require.Equal(t, []byte{1, 2, 3, 4, 5, 6}, s.buf)
	})
}

var errSinkRefused = errors.New("sink refused the write")

// TestV3WriterHoldsTheFirstErrorAndStopsWriting covers the error-holding
// contract: the first failure is the only one that carries information, so
// nothing further is written and the error names the write that failed.
//
// TWO WINDOWS ARE LOADED BEFORE THE FLUSH, and that is the whole point of the
// setup. With one, the guard in writeThrough is unobservable: put already
// refuses to route a store once the error is held, so a single window can never
// reach the sink twice anyway. With two, flushAll walks to the SECOND window
// after the first has failed, and only writeThrough's own check stops it —
// which matters because a write landing after a failure produces a file with
// later sections present and earlier ones missing, the one shape the
// discard-whole discipline exists to prevent.
func TestV3WriterHoldsTheFirstErrorAndStopsWriting(t *testing.T) {
	s := &recordingSink{failWrite: func(int64, int) error { return errSinkRefused }}
	w := newV3Writer(s)

	w.put(0, []byte{1, 2, 3, 4})
	w.put(v3WriteRunBytes*4, []byte{5, 6, 7, 8}) // a second live window, far away
	require.Zero(t, s.attempts, "control: neither window may have reached the sink before the flush")

	w.flushAll()
	first := w.err
	require.Error(t, first)
	require.ErrorIs(t, first, errSinkRefused, "the sink's error must be wrapped, not replaced")
	require.Contains(t, first.Error(), "hnsw encode: writing 4 bytes at 0",
		"the held error must name the write that failed, not merely that one did")
	require.Equal(t, 1, s.attempts,
		"flushAll attempted %d writes after the first one failed — a store landing after a failure leaves a file "+
			"with later sections written and earlier ones missing", s.attempts)

	// Every later store, and every later flush, is a no-op: nothing reaches the
	// sink and the held error is not replaced by a subsequent one.
	w.put(64, []byte{9, 9, 9, 9})
	w.flushAll()
	require.Equal(t, first, w.err, "a later failure replaced the first one, which is the one that carries information")
	require.Equal(t, 1, s.attempts, "the writer touched the sink again after it had already failed")
	require.Empty(t, s.calls, "a refused write must leave the sink's contents untouched")
}

// encodeToRecorder runs the real emitter into a recording sink and returns what
// it wrote alongside the length it reported.
func encodeToRecorder(t *testing.T, h *binaryGraph) (*recordingSink, int64) {
	t.Helper()
	s := &recordingSink{}
	n, err := encodeGraphV3To(s, h)
	require.NoError(t, err)
	return s, n
}

// TestEncodeGraphV3ToWritesExactlyTheBlobAtEveryBoundary drives the emitter over
// the shapes where padding could push the output past its own reported length.
//
// THE HIGH-WATER MARK IS THE ASSERTION. Gap padding is only ever inserted BEFORE
// a following store, so the last byte written must still be the last byte of the
// blob; if it were not, the engine's Truncate to the reported length would cut
// live bytes off the end of a segment. The recording sink grows to fit, so its
// length IS the high-water mark.
func TestEncodeGraphV3ToWritesExactlyTheBlobAtEveryBoundary(t *testing.T) {
	// The id widths walk the alignment gap before the layer-offset array through
	// every value it can take: at three nodes, 64 + 3*16 + 3*len puts the array's
	// start 1, 2, 3 and 0 bytes past the id bytes in turn.
	for _, tc := range []struct {
		name    string
		ids     []string
		dtype   byte
		vecLen  int
		wantGap int
	}{
		{"no nodes at all", nil, dtypeUbinary, defaultVecBytes, 0},
		{"a single node", []string{"only-one"}, dtypeUbinary, defaultVecBytes, 0},
		{"a 1-byte alignment gap", []string{"aa000", "aa001", "aa002"}, dtypeUbinary, defaultVecBytes, 1},
		{"a 2-byte alignment gap", []string{"aa0000", "aa0001", "aa0002"}, dtypeUbinary, defaultVecBytes, 2},
		{"a 3-byte alignment gap", []string{"aa00000", "aa00001", "aa00002"}, dtypeUbinary, defaultVecBytes, 3},
		{"no alignment gap", []string{"aa000000", "aa000001", "aa000002"}, dtypeUbinary, defaultVecBytes, 0},
		{"float32 vectors", []string{"f32-a", "f32-b", "f32-c"}, dtypeFloat32, 64, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]binaryBuildItem, len(tc.ids))
			idBytes := 0
			for i, id := range tc.ids {
				items[i] = binaryBuildItem{id: id, vec: fixtureVec(tc.dtype, tc.vecLen, i)}
				idBytes += len(id)
			}
			h := buildBinaryHNSWSerialDeterministic(items, tc.vecLen, tc.dtype, defaultM, defaultEfConstruction)

			// The control on the fixture, not on the writer: assert the gap this
			// case exists to exercise is the gap the layout actually opens.
			layout, err := sizeGraphV3(h)
			require.NoError(t, err)
			gotGap := layout.layerOffsetsOff - (layout.idBytesOff + idBytes)
			require.Equalf(t, tc.wantGap, gotGap,
				"the fixture opens a %d-byte alignment gap, not the %d this case is written for", gotGap, tc.wantGap)

			s, n := encodeToRecorder(t, h)
			require.EqualValues(t, layout.blobLen, n, "the emitter reported a length other than the layout's")
			require.Len(t, s.buf, int(n),
				"the encode's high-water mark is %d but it reported %d bytes — padding ran past the blob's end, and "+
					"the engine's Truncate to the reported length would cut live bytes off the segment",
				len(s.buf), n)

			seg, err := Format{}.Decode(s.buf)
			require.NoError(t, err, "the buffered encode produced bytes the reader refuses")
			require.Len(t, seg.IDs(), len(tc.ids))
		})
	}
}

// fixtureVec builds one deterministic vector of the width and dtype under test.
//
// THE FLOAT32 ARM WRITES REAL FLOATS rather than arbitrary bytes, and it has to:
// a float32 block is read through a typed view and scored by dot product, so a
// bit pattern that happened to decode as NaN would make the builder's distance
// comparisons meaningless and the fixture's graph shape unreproducible.
func fixtureVec(dtype byte, vecBytes, ord int) []byte {
	vec := make([]byte, vecBytes)
	if dtype == dtypeFloat32 {
		for b := 0; b+4 <= vecBytes; b += 4 {
			binary.LittleEndian.PutUint32(vec[b:], math.Float32bits(float32((ord*31+b*7)%251)/251))
		}
		return vec
	}
	for b := range vec {
		vec[b] = byte((ord*31 + b*7) % 251)
	}
	return vec
}

// TestEncodeGraphV3ToFailsAtTheFlushBarriers is THE error arm that a buffered
// writer introduces and an unbuffered one cannot have.
//
// checksumRange reads the SINK back rather than any buffer, so a flush that
// failed before it would leave the CRC covering bytes nobody wrote — a write
// failure turned into a corrupt-looking success. The first barrier must
// therefore return BEFORE any read happens, and the read count is how that is
// observed rather than asserted.
func TestEncodeGraphV3ToFailsAtTheFlushBarriers(t *testing.T) {
	fixture := func(t *testing.T) (*binaryGraph, v3Layout) {
		t.Helper()
		h := buildBinaryHNSWSerialDeterministic(
			sizedBuildItems(t, 64, defaultVecBytes), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)
		layout, err := sizeGraphV3(h)
		require.NoError(t, err)
		return h, layout
	}

	t.Run("a flush failing before the checksum returns without reading the sink", func(t *testing.T) {
		h, _ := fixture(t)
		s := &recordingSink{failWrite: func(int64, int) error { return errSinkRefused }}

		n, err := encodeGraphV3To(s, h)
		require.Error(t, err)
		require.ErrorIs(t, err, errSinkRefused)
		require.Zero(t, n)
		require.Zero(t, s.reads,
			"the emitter checksummed a blob whose bytes never landed — that turns a write failure into a "+
				"segment that passes its own self-check")
	})

	t.Run("a footer flush failing returns after the checksum ran", func(t *testing.T) {
		h, layout := fixture(t)
		s := &recordingSink{failWrite: func(off int64, _ int) error {
			if off >= int64(layout.crcOff) {
				return errSinkRefused
			}
			return nil
		}}

		n, err := encodeGraphV3To(s, h)
		require.Error(t, err)
		require.ErrorIs(t, err, errSinkRefused)
		require.Zero(t, n)
		require.Positive(t, s.reads,
			"control: this arm is meant to fail at the SECOND barrier, after checksumRange read the sink — "+
				"zero reads means it failed at the first and the arm is untested")
		require.Contains(t, err.Error(), fmt.Sprintf("writing 4 bytes at %d", layout.crcOff),
			"the error must name the footer store that failed")
	})
}
