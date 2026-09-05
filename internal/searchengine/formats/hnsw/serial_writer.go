// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// serial_writer.go carries the serialVersion-3 blob's WRITER: the
// offset-addressed store helpers and the coalescing windows that keep an
// encode's syscall count proportional to flushes rather than to encoded values.
// The layout it writes into is in serial_layout.go, and the emission that drives
// it is in serial.go.
//
// IT IS ITS OWN FILE FOR THE SAME REASON THE SIBLING FORMAT'S IS: bm25 splits
// merge_writer.go from merge_stream.go along exactly this seam. The windows are
// a self-contained mechanism with their own invariants — one backing
// allocation, one window per live cursor, a run that stays live across its own
// flushes — and they are read and reviewed as a unit rather than interleaved
// with the format's emission order.

const (
	// v3WriteRunBytes is how much of one window is held before it is written out.
	// It is a FIXED window and is never derived from the segment size: a buffer
	// sized from the output would reintroduce exactly the output-sized allocation
	// this emitter exists to remove.
	//
	// IT IS EIGHT TIMES THE SIBLING WRITER'S WINDOW, AND THAT IS A DESIGN RATHER
	// THAN A DRIFT. bm25's mergeRunBytes is 8 KiB because its windows are charged
	// against TestStreamedMergePeakHeapBounded, which bounds allocation against
	// the OUTPUT SIZE; that bound is a property of bm25's algorithm, which can
	// stream a merge, and hnsw has no counterpart to it — this format re-inserts
	// every survivor into a fresh graph whose vector block is output-sized
	// regardless. Copying 8 KiB here would copy a constraint that does not apply
	// rather than a design. 64 KiB is the size the encode already pays once for
	// the checksum read-back (v3CRCChunk), so the writer's whole footprint is a
	// small multiple of a buffer this function already holds.
	//
	// THE SIZE IS WHAT MAKES THE BOUND HOLD AT PRODUCTION SCALE. Replaying a
	// 1,024-node merge's recorded write sequence: four 4 KiB windows leave 79
	// calls for a 340,572-byte segment, four 64 KiB windows leave 8 — exactly
	// ceil(bytes/65536) plus two.
	v3WriteRunBytes = 64 << 10

	// v3WriteRunSlots is how many contiguous windows stay open at once, and it is
	// sized from the emission pattern rather than picked round.
	//
	// THE FILL ADVANCES EXACTLY FOUR INDEPENDENTLY-ASCENDING CURSORS and
	// interleaves them node by node: the node directory's entry offset, the id
	// bytes, the layer-offset array and the neighbor arena. Each is ascending
	// within itself but interleaved with the others, so a single window would be
	// evicted on every switch and coalesce nothing. One window per live cursor is
	// what turns that pattern into whole-window writes.
	//
	// THE HEADER AND THE THREE TRAILING SECTIONS NEED NO SLOTS OF THEIR OWN, and
	// that is a property of the layout rather than luck: nodeDirOff is
	// align(v3HeaderSize, 4) and v3HeaderSize is already 4-aligned, so the first
	// node-directory entry lands exactly where the header ended and extends its
	// window; and the id directory, the vector block and the footer CRC each
	// begin within an alignment gap of the section before them, so they continue
	// the arena's window in turn. A fifth and sixth slot buy nothing, measured:
	// that same replay leaves 8 calls at four windows and 8 at six.
	v3WriteRunSlots = 4

	// v3WriteRunMaxGap is the largest forward gap a window absorbs as zero
	// padding rather than breaking on. It is the largest gap THIS FORMAT can
	// open: v3EntReserved sits at 12 in a 16-byte entry, so the node directory
	// leaves 4 unwritten bytes per node; v3HdrReserved2 leaves 2 in the header;
	// and every section boundary is align(..., 4), so an alignment can open at
	// most 3.
	//
	// IT IS NOT OPTIONAL. Without it the same four windows leave 1,032 calls
	// instead of 8 on that replay, because the 4-byte reserved field breaks the
	// node-directory stream into one run per node.
	//
	// The padding is byte-identical to what the destination held before, because
	// every MergeSink destination starts zero-filled — a fresh scratch file from
	// os.CreateTemp, a fresh os.Create in the tests, and encodeGraphV3's own
	// make([]byte, blobLen). An unwritten gap in a sparse file reads as zeros,
	// which is the property the read-back checksum already depended on.
	v3WriteRunMaxGap = 8
)

// v3Writer places fixed-width values at absolute blob offsets, coalescing them
// into a small set of fixed windows and holding the first error rather than
// checking at every store. The discipline is bm25's mergeWriter's: a partial
// blob is discarded whole, so only the first failure carries information.
//
// WHY IT BUFFERS AT ALL. Every store is one WriteAt on the sink, and in
// production that sink is a file, so an unbuffered writer spends one pwrite(2)
// per encoded value: measured, 18,277 of them for an 82,828-byte segment and
// 73,625 for a 331,356-byte one, linear in node count. The windows turn that
// into one write per filled window — 9 for that same 331,356-byte segment.
type v3Writer struct {
	dst searchengine.MergeSink
	err error
	// runs are the open coalescing windows. See v3WriteRunSlots for why there are
	// several and why the number is what it is.
	runs [v3WriteRunSlots]v3WriteRun
	// backing is the single allocation every window is sliced out of.
	backing []byte
	// rr is the round-robin eviction cursor.
	rr int
	// passStart, passEnd and passed are the same hull for stores written
	// straight through, which belong to no window and would otherwise be a range
	// nothing remembers.
	passStart int64
	passEnd   int64
	passed    bool
	// arms records which routing arms this writer's stores took. See v3WriteArms.
	arms v3WriteArms
	// scratch backs the fixed-width stores so a putU32 in the per-neighbor inner
	// loop does not allocate.
	scratch [8]byte
	// strBuf carries id bytes to the writer without a per-node conversion.
	strBuf []byte
}

// v3WriteArms counts how many stores took each of put's arms, and why a
// candidate window was declined. It is the differential census's REACH
// INSTRUMENT: what lets serial_writer_reference_test.go assert that it exercised
// every arm rather than assume it.
//
// WHY THE WRITER CARRIES IT AND NOT THE TEST. Which arm a store took is not
// observable from outside. The sink sees write calls, and several arms produce
// the same call shape — a declined extend and an ordinary eviction are one new
// window each, and an in-range overwrite and a coalesced extend are both no call
// at all. A test that recovered the arm from the store sequence would be a
// second copy of the router, and a second copy agrees with a wrong original.
//
// WHY IT IS WORTH THE FIELDS. Three review rounds each found a guard in this
// writer whose removal left the whole package green, and each time the reason
// was the same: the census the writer's correctness argument rests on never
// reached the arm the guard protects, so nothing failed when the guard went.
// Counting reach turns that from something a reader has to take on faith into
// something the census fails on. The cost is eight ints on a struct allocated
// once per encode and one increment per store; no arm READS a counter, so
// nothing here can change which bytes the destination ends up holding.
type v3WriteArms struct {
	// oversize, inRange, extend and evict are put's four routing arms, in the
	// order put tries them.
	oversize int
	inRange  int
	extend   int
	evict    int
	// flushOverflow is the extend arm's flush-before-overflow guard firing: a
	// store that fits a window but not the one already holding bytes.
	flushOverflow int
	// declinedGap, declinedSlot and declinedPass are why a live window was NOT
	// extended: the gap was negative or wider than the absorb, another slot's
	// coverage hull intersected the span the extend would newly cover, or a store
	// written straight through already covered it.
	declinedGap  int
	declinedSlot int
	declinedPass int
}

// v3WriteRun is one open, contiguous window of output held back from the sink.
//
// A RUN STAYS OPEN ACROSS ITS OWN FLUSHES, with start advanced past what was
// written and buf emptied. That is what keeps a cursor on the same slot: if a
// flush closed the run, the next store in that stream would fall through to
// round-robin eviction and could land on a different slot every window, which
// would break exactly the per-cursor affinity the slots exist to provide.
type v3WriteRun struct {
	start int64
	buf   []byte
	live  bool
	// covStart and covEnd are the HULL of every range this slot has ever
	// covered: bytes it still holds, bytes it has already flushed to the sink,
	// and bytes a previous occupant of the slot wrote before it was evicted.
	// covered says whether the slot has covered anything at all.
	//
	// IT IS A HULL AND IT IS NEVER FORGOTTEN, and both halves are the point. An
	// exact record of covered ranges would grow with the number of stores, which
	// is the unbounded allocation this writer exists to avoid; a hull is two
	// integers per slot and can only ever OVER-state what a slot covered. Over-
	// stating costs write calls and never costs bytes, which is the direction a
	// safety bound has to err in. Forgetting, by contrast, is what made the
	// first two versions of this writer lose data: bytes already in the sink are
	// invisible to a check that reads only what a window is currently holding.
	covStart int64
	covEnd   int64
	covered  bool
}

// newV3Writer prepares a writer whose windows are already at full capacity.
//
// ONE BACKING ARRAY, SLICED, AND THAT IS THE POINT. Letting each window grow
// itself through append would allocate every intermediate size on the way to the
// window bound, and it is charged against the encoder's landed allocation gates:
// TestEncodeGraphV3ToAllocatesItsWindowsOncePerWriter reads 5 allocations for a
// whole encode, of which this make is the one the windows cost. Handing each run
// a zero-length slice with cap exactly v3WriteRunBytes makes every append a copy
// into memory that already exists.
func newV3Writer(dst searchengine.MergeSink) *v3Writer {
	w := &v3Writer{dst: dst, backing: make([]byte, v3WriteRunSlots*v3WriteRunBytes)}
	for i := range w.runs {
		lo := i * v3WriteRunBytes
		w.runs[i].buf = w.backing[lo : lo : lo+v3WriteRunBytes]
	}
	return w
}

// put places b at off, coalescing it into an open window where it can.
//
// THE ROUTING ORDER IS LOAD-BEARING IN ONE PLACE, and one only.
//
// The OVERSIZE arm is tested FIRST, before the extend arm, and that ordering is
// this format's rather than the sibling writer's. The vector block is one store
// that begins exactly where the id directory ended, so an extend arm reached
// first would flush the window and then append the whole block into a slice
// whose capacity is one window — and append would silently allocate a fresh,
// OUTPUT-SIZED backing array, which is the one allocation this emitter exists
// not to make. Passing it through costs the same single write and allocates
// nothing.
//
// An IN-RANGE store is matched before a contiguous-extend so that a backpatch
// into a window that still holds the bytes stays a memory write. That is a
// choice about write CALLS and not about correctness: a store wholly inside a
// window has a NEGATIVE gap against that window, so the extend arm skips it
// whichever order the two are tried in, and the coverage check declines an
// extend over any range another slot covers. Inverting the two arms therefore
// changes what the writer does, not what the destination ends up holding — a
// property of the arms' own conditions, which the census cannot state because it
// runs one arm order.
//
// THE INVARIANT: the destination ends up holding exactly what a plain
// offset-addressed destination would hold for the same stores, whatever order
// the windows flush in. TWO things enforce it.
//
// flushOverlapping, in the two arms that open a window or bypass one, writes out
// any window still HOLDING bytes in the range about to be written, so the older
// bytes reach the sink before the newer ones rather than after.
//
// The extend arm declines any candidate whose NEW SPAN — the absorbed GAP
// included, not only the store — lies in a range any slot has ever covered.
// Declining sends the store to the eviction arm, which flushes the conflict
// first and lands the bytes in the order they were stored.
//
// WHY THE GAP HAS TO BE COUNTED, AND WHY "EVER COVERED" RATHER THAN "STILL
// HOLDS". Absorbed padding is not data. It stands in for a destination that
// reads as zeros, which is true of a range nothing has written and false of
// every other range. Two versions of this writer got that wrong in two
// different ways: the first ignored the gap entirely and padded over bytes
// another window was holding; the second counted the gap but asked only which
// windows were HOLDING bytes, so a window that had already flushed — its bytes
// sitting in the sink, its buffer empty — was invisible, and the padding went
// over them. A window that has flushed still owns what it wrote.

func (w *v3Writer) put(off int, b []byte) {
	if w.err != nil || len(b) == 0 {
		return
	}
	start := int64(off)
	end := start + int64(len(b))

	// (1) Too large to fit a window alongside a maximal gap: straight through.
	if len(b)+v3WriteRunMaxGap > v3WriteRunBytes {
		w.arms.oversize++
		w.flushOverlapping(start, end)
		w.writeThrough(start, b)
		w.recordPassThrough(start, end)
		return
	}

	// (2) Wholly inside an open window: overwrite in place.
	for i := range w.runs {
		r := &w.runs[i]
		if len(r.buf) > 0 && start >= r.start && end <= r.start+int64(len(r.buf)) {
			w.arms.inRange++
			copy(r.buf[start-r.start:], b)
			return
		}
	}

	// (3) Extends an open window, possibly across an absorbed gap. An
	// emptied-but-live window matches here too, which is what keeps a cursor on
	// its own slot across the flushes it triggers.
	for i := range w.runs {
		r := &w.runs[i]
		if !r.live {
			continue
		}
		gap := start - (r.start + int64(len(r.buf)))
		if gap < 0 || gap > v3WriteRunMaxGap {
			w.arms.declinedGap++
			continue
		}
		// The span this window would newly cover — the absorbed gap included —
		// must lie in a range no slot has ever covered. See the paragraph above
		// put. Self is checked too and costs nothing on an ordinary extension,
		// whose span begins exactly where this slot's coverage ends.
		if w.coveredElsewhere(r.start+int64(len(r.buf)), end) {
			continue
		}
		// FLUSH BEFORE IT WOULD OVERFLOW, never after. Each window is a slice into
		// one pre-sized backing array with cap exactly v3WriteRunBytes, so
		// appending past that cap would make append allocate a fresh buffer,
		// reintroducing per-store heap growth. Flushing first leaves len zero, and
		// arm (1) has already guaranteed that a maximal gap plus b fits.
		if len(r.buf)+int(gap)+len(b) > v3WriteRunBytes {
			w.arms.flushOverflow++
			w.flushRun(r)
			gap = start - r.start
		}
		for range gap {
			r.buf = append(r.buf, 0)
		}
		r.buf = append(r.buf, b...)
		r.cover(start, end)
		w.arms.extend++
		if len(r.buf) >= v3WriteRunBytes {
			w.flushRun(r)
		}
		return
	}

	// (4) Open a new window, evicting round-robin.
	w.arms.evict++
	w.flushOverlapping(start, end)
	r := &w.runs[w.rr]
	w.rr = (w.rr + 1) % v3WriteRunSlots
	w.flushRun(r)
	r.start = start
	r.buf = append(r.buf[:0], b...)
	r.live = true
	r.cover(start, end)
}

// cover widens the slot's coverage hull to include [from, to).
func (r *v3WriteRun) cover(from, to int64) {
	if !r.covered {
		r.covStart, r.covEnd, r.covered = from, to, true
		return
	}
	r.covStart = min(r.covStart, from)
	r.covEnd = max(r.covEnd, to)
}

// recordPassThrough widens the hull for stores that went straight to the sink.
func (w *v3Writer) recordPassThrough(from, to int64) {
	if !w.passed {
		w.passStart, w.passEnd, w.passed = from, to, true
		return
	}
	w.passStart = min(w.passStart, from)
	w.passEnd = max(w.passEnd, to)
}

// coveredElsewhere reports whether [from, to) lies in a range any slot has ever
// covered, or in one written straight through.
//
// IT IS THE ONE QUESTION THE EXTEND ARM HAS TO ASK, and asking it of coverage
// rather than of held bytes is the whole difference: bytes a window flushed are
// in the destination, where padding would overwrite them and nothing would
// notice.
func (w *v3Writer) coveredElsewhere(from, to int64) bool {
	if w.passed && from < w.passEnd && w.passStart < to {
		w.arms.declinedPass++
		return true
	}
	for i := range w.runs {
		r := &w.runs[i]
		if r.covered && from < r.covEnd && r.covStart < to {
			w.arms.declinedSlot++
			return true
		}
	}
	return false
}

// flushOverlapping writes out any open window intersecting [off, end) so a new
// window, or a store passed straight through, cannot end up racing a stale copy
// of the same bytes.
func (w *v3Writer) flushOverlapping(off, end int64) {
	for i := range w.runs {
		r := &w.runs[i]
		if len(r.buf) > 0 && off < r.start+int64(len(r.buf)) && r.start < end {
			w.flushRun(r)
		}
	}
}

// flushRun writes one window out and reopens it empty at the offset it reached.
func (w *v3Writer) flushRun(r *v3WriteRun) {
	if len(r.buf) == 0 {
		return
	}
	w.writeThrough(r.start, r.buf)
	r.start += int64(len(r.buf))
	r.buf = r.buf[:0]
}

// flushAll writes every open window out.
//
// IT IS WHAT MAKES THE SINK COMPLETE, and encodeGraphV3To calls it before BOTH
// of its error barriers. The checksum reads the SINK back rather than any
// buffer, so bytes still held in a window at that point would be invisible to
// the CRC and the footer would describe a blob nobody wrote.
func (w *v3Writer) flushAll() {
	for i := range w.runs {
		w.flushRun(&w.runs[i])
	}
}

// writeThrough is the ONLY place this writer touches the sink.
func (w *v3Writer) writeThrough(off int64, b []byte) {
	if w.err != nil {
		return
	}
	if _, err := w.dst.WriteAt(b, off); err != nil {
		w.err = fmt.Errorf("hnsw encode: writing %d bytes at %d: %w", len(b), off, err)
	}
}

func (w *v3Writer) putString(off int, s string) {
	// The id goes out through a reusable buffer, following bm25 mergeWriter's
	// strBuf: ids are written one per node and in huge numbers, so a []byte(s) at
	// each of them would allocate proportionally to the corpus — which is exactly
	// the cost this emitter exists to avoid.
	//
	// REUSING IT IS SAFE BECAUSE NOTHING RETAINS THE SLICE. Both arms of put copy
	// out of b before returning: the buffered arms copy into a window, and the
	// pass-through arm hands it to WriteAt, which io.WriterAt requires not to
	// retain it. A window that aliased this buffer instead would emit the LAST
	// node's id in every id slot.
	w.strBuf = append(w.strBuf[:0], s...)
	w.put(off, w.strBuf)
}

func (w *v3Writer) putByte(off int, v byte) {
	w.scratch[0] = v
	w.put(off, w.scratch[:1])
}

func (w *v3Writer) putU16(off int, v uint16) {
	binary.LittleEndian.PutUint16(w.scratch[:2], v)
	w.put(off, w.scratch[:2])
}

func (w *v3Writer) putU32(off int, v uint32) {
	binary.LittleEndian.PutUint32(w.scratch[:4], v)
	w.put(off, w.scratch[:4])
}
