// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_writer.go carries the merged segment's WRITER: the offset-addressed store
// helpers and the coalescing windows that keep a merge's syscall count
// proportional to flushes rather than to fields. The layout it writes into, and
// the emission that drives it, are in merge_stream.go.

// mergeWriter appends to the tail of an output file and patches earlier offsets
// in place. Errors are held and reported once at the end rather than checked at
// every store: a partial file is discarded whole, so the first failure is the
// only one that carries information.
type mergeWriter struct {
	f    searchengine.MergeSink
	tail int64
	err  error
	// strBuf carries string payloads to WriteAt without a per-call conversion.
	// Terms and member ids are written one at a time and in huge numbers, so a
	// []byte(s) at each of them would allocate proportionally to the corpus —
	// which is exactly the cost this writer exists to avoid.
	strBuf []byte
	// runs are the open coalescing windows. See mergeRunSlots for why there are
	// several and why the number is what it is.
	runs [mergeRunSlots]mergeRun
	// backing is the single allocation every window is sliced out of.
	backing []byte
	// scratch backs the fixed-width patches. See the paragraph above patchU16.
	scratch [8]byte
	// rr is the round-robin eviction cursor.
	rr int
}

// mergeRun is one open, contiguous window of output held back from the sink.
//
// A run STAYS OPEN ACROSS ITS OWN FLUSHES, with start advanced past what was
// written and buf emptied. That is what keeps a stream on the same slot: if a
// flush closed the run, the next write in that stream would fall through to
// round-robin eviction and could land on a different slot every window, which
// would break exactly the per-stream affinity the slots exist to provide.
type mergeRun struct {
	start int64
	buf   []byte
	live  bool
}

const (
	// mergeRunBytes is how much of one run is held before it is written out. It
	// is a FIXED window and is never derived from the segment size: a buffer sized
	// from the output would reintroduce exactly the output-sized allocation this
	// merge path exists to remove.
	//
	// IT IS SMALL BECAUSE THE WHOLE WINDOW SET IS CHARGED AGAINST A LANDED BOUND.
	// TestStreamedMergePeakHeapBounded requires the writer to allocate less than
	// half the segment it produces, and the windows are allocated whether or not
	// they fill. At 32 KiB per window the eight of them cost 256 KiB, which is
	// within a few KiB of half the 528 KiB fixture that bound uses — measured, not
	// estimated: the first draft of this writer took that test from 131,032 bytes
	// to 572,504 and turned it red. 8 KiB per window still turns thousands of
	// per-field writes into tens of flushes, which is the property the buffering
	// is for.
	mergeRunBytes = 8 << 10

	// mergeRunSlots is how many contiguous windows stay open at once, and it is
	// sized from the emission pattern rather than picked round.
	//
	// THE EMITTER WRITES SEVERAL INDEPENDENTLY-ASCENDING STREAMS AT ONCE and
	// interleaves them term by term. Per (term, field) it appends the posting run
	// and the term bytes at the TAIL, then patches a 16-byte dictionary row inside
	// THAT FIELD'S entries region; per term it also patches a row in the docFreq
	// region. Each of those is ascending within itself — successive rows in a
	// field are adjacent — but they are interleaved with each other, so a single
	// window would be evicted on every switch and coalesce nothing. One window per
	// live stream is what turns that pattern into whole-window writes: five fields,
	// the docFreq rows, the tail, and one spare for the header and dictionary-header
	// backpatches.
	mergeRunSlots = 8

	// mergeRunMaxGap is the largest forward gap a run absorbs as zero padding
	// rather than breaking on. Appends land at alignments of at most 4, so the gap
	// an alignment can open is at most 3; padding it keeps the tail one run
	// instead of one run per aligned append. The padding is byte-identical to what
	// the file held before, because an unwritten gap in a sparse file reads as
	// zeros — which is the property the writer already depended on.
	mergeRunMaxGap = 8
)

// newMergeWriter prepares a writer whose windows are already at full capacity.
//
// ONE BACKING ARRAY, SLICED, AND THAT IS THE POINT. Letting each window grow
// itself through append would allocate every intermediate size on the way to the
// window bound — measured at roughly 64 KiB of garbage per 32 KiB window — and
// it is charged against the writer's landed allocation bound. Handing each run a
// zero-length slice with cap exactly mergeRunBytes makes every append a copy
// into memory that already exists.
func newMergeWriter(f searchengine.MergeSink, tail int64) *mergeWriter {
	w := &mergeWriter{f: f, tail: tail, backing: make([]byte, mergeRunSlots*mergeRunBytes)}
	for i := range w.runs {
		lo := i * mergeRunBytes
		w.runs[i].buf = w.backing[lo : lo : lo+mergeRunBytes]
	}
	return w
}

// store places b at off, coalescing it into an open run where it can.
//
// THE ROUTING ORDER IS LOAD-BEARING, IN BOTH OF ITS RELATIONS.
//
// An in-range write is matched BEFORE everything else, so a write landing
// exactly at another run's start is absorbed by that run rather than growing
// this one on top of it, and the two runs cannot then disagree about those
// bytes. That is a statement about this arm, not a global one: the extend arm
// does not flush overlapping runs, so a store that extends one run across
// another's range still leaves both live and overlapping. flushOverlapping on
// arms (2) and (4) is what keeps such a pair from reaching the sink out of
// order.
//
// A pass-through write is matched BEFORE a contiguous-extend, and THAT relation
// is what keeps every window inside the single backing array newMergeWriter
// hands out. A store larger than a window that arrives contiguous with a live
// run matches both arms; extend-first takes it, flushes the run to make room,
// and then appends len(b) bytes onto a slice whose cap is exactly mergeRunBytes
// — so append allocates, and flushRun's r.buf = r.buf[:0] retains the fresh
// array for the rest of the writer's life. Measured on this package's own suite
// while the arms were the other way round: 17 such stores per suite run, 15
// fresh reallocations totaling 271,616 bytes, and up to 2 of the 8 slots
// detached at once. This order costs one extra sink write per contiguous
// oversize store — 47 rather than 46 on a production-shape merge — and that is
// the trade that buys back the fixed allocation.
func (w *mergeWriter) store(off int64, b []byte) {
	if w.err != nil || len(b) == 0 {
		return
	}
	end := off + int64(len(b))

	// (1) Wholly inside an open run: overwrite in place. This is how a backpatch
	// of a value the same run already carries stays a memory write.
	for i := range w.runs {
		r := &w.runs[i]
		if len(r.buf) > 0 && off >= r.start && end <= r.start+int64(len(r.buf)) {
			copy(r.buf[off-r.start:], b)
			return
		}
	}

	// (2) A write too large to fit a window alongside a maximal alignment gap is
	// passed straight through: buffering it would grow a run past the bound the
	// fixed window exists to enforce. It is tested BEFORE (3), which is the
	// relation the routing order above exists to state.
	if len(b)+mergeRunMaxGap > mergeRunBytes {
		w.flushOverlapping(off, end)
		w.writeThrough(off, b)
		return
	}

	// (3) Extends an open run, possibly across a small alignment gap. An
	// emptied-but-live run matches here too, which is what keeps a stream on its
	// own slot across the flushes it triggers.
	for i := range w.runs {
		r := &w.runs[i]
		if !r.live {
			continue
		}
		gap := off - (r.start + int64(len(r.buf)))
		if gap < 0 || gap > mergeRunMaxGap {
			continue
		}
		// FLUSH BEFORE IT WOULD OVERFLOW, never after. Each window is a slice into
		// one pre-sized backing array with cap exactly mergeRunBytes, so appending
		// past that cap would make append allocate a fresh buffer — reintroducing
		// per-write heap growth, which is the cost this whole mechanism exists to
		// remove. Flushing first leaves len zero, and case (2) guarantees what
		// follows fits: anything reaching here failed (2)'s predicate, so
		// len(b)+mergeRunMaxGap <= mergeRunBytes, and flushRun advances start by
		// exactly what it wrote, so the recomputed gap is the same gap the bound
		// above already accepted.
		if len(r.buf)+int(gap)+len(b) > mergeRunBytes {
			w.flushRun(r)
			gap = off - r.start
		}
		for range gap {
			r.buf = append(r.buf, 0)
		}
		r.buf = append(r.buf, b...)
		if len(r.buf) >= mergeRunBytes {
			w.flushRun(r)
		}
		return
	}

	// (4) Open a new run, evicting round-robin.
	w.flushOverlapping(off, end)
	r := &w.runs[w.rr]
	w.rr = (w.rr + 1) % mergeRunSlots
	w.flushRun(r)
	r.start = off
	r.buf = append(r.buf[:0], b...)
	r.live = true
}

// flushOverlapping writes out any open run intersecting [off, end) so a new run
// or a pass-through write cannot end up racing a stale copy of the same bytes.
func (w *mergeWriter) flushOverlapping(off, end int64) {
	for i := range w.runs {
		r := &w.runs[i]
		if len(r.buf) > 0 && off < r.start+int64(len(r.buf)) && r.start < end {
			w.flushRun(r)
		}
	}
}

// flushRun writes one run out and reopens it empty.
func (w *mergeWriter) flushRun(r *mergeRun) {
	if len(r.buf) == 0 {
		return
	}
	w.writeThrough(r.start, r.buf)
	r.start += int64(len(r.buf))
	r.buf = r.buf[:0]
}

// flushAll writes every open run out. It is what makes the sink complete, and
// streamMergeToFile calls it before reporting a length.
func (w *mergeWriter) flushAll() {
	for i := range w.runs {
		w.flushRun(&w.runs[i])
	}
}

// writeThrough is the ONLY place this writer touches the sink.
func (w *mergeWriter) writeThrough(off int64, b []byte) {
	if w.err != nil {
		return
	}
	if _, err := w.f.WriteAt(b, off); err != nil {
		w.err = fmt.Errorf("bm25 merge: write %d bytes at %d: %w", len(b), off, err)
	}
}

// patch writes b at an already-planned offset in the fixed prefix.
func (w *mergeWriter) patch(off int, b []byte) {
	w.store(int64(off), b)
}

// THE THREE FIXED-WIDTH PATCHES GO THROUGH w.scratch, NOT A LOCAL ARRAY, and
// that is a consequence of the sink being an interface rather than an *os.File.
// A local `var b [4]byte` whose slice reaches an interface method ESCAPES: the
// compiler cannot see which implementation receives it or whether that
// implementation retains it, so it heap-allocates. These three are called once
// per dictionary row and once per docFreq row, so the escape is per-term —
// measured at roughly 72 KB of garbage on a 528 KB merge, which is most of the
// way to the writer's landed allocation bound on its own. A field on the
// already-heap-allocated writer costs one allocation for the whole merge.

func (w *mergeWriter) patchU16(off int, v uint16) {
	binary.LittleEndian.PutUint16(w.scratch[:2], v)
	w.patch(off, w.scratch[:2])
}

func (w *mergeWriter) patchU32(off int, v uint32) {
	binary.LittleEndian.PutUint32(w.scratch[:4], v)
	w.patch(off, w.scratch[:4])
}

func (w *mergeWriter) patchU64(off int, v uint64) {
	binary.LittleEndian.PutUint64(w.scratch[:8], v)
	w.patch(off, w.scratch[:8])
}

// appendAligned writes b at the next tail offset that satisfies alignTo and
// returns where it landed. Gaps left by alignment read back as zeros, which is
// what keeps the output byte-identical between runs — the run buffer writes
// those gap bytes out as explicit zeros rather than leaving them unwritten, which
// is the same bytes by a different route.
func (w *mergeWriter) appendAligned(b []byte, alignTo int) int {
	if w.err != nil {
		return 0
	}
	at := int64(align(int(w.tail), alignTo))
	w.store(at, b)
	w.tail = at + int64(len(b))
	return int(at)
}

// appendStr appends a string's bytes through the writer's reusable buffer.
func (w *mergeWriter) appendStr(s string, alignTo int) int {
	w.strBuf = append(w.strBuf[:0], s...)
	return w.appendAligned(w.strBuf, alignTo)
}

// patchStr writes a string's bytes at a planned offset.
func (w *mergeWriter) patchStr(off int, s string) {
	w.strBuf = append(w.strBuf[:0], s...)
	w.patch(off, w.strBuf)
}
