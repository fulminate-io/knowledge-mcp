// SPDX-License-Identifier: Apache-2.0

// runarena.go — bump-allocator backing for TextRun's per-glyph slice
// fields (Glyphs, CharBounds, CharFlags).
//
// Without an arena every emit-time appendRun allocates three small
// slices: []uint16 for Glyphs, []Rect for CharBounds, []uint8 for
// CharFlags. For documents with hundreds of thousands of glyph runs
// (DDIA: ~50k runs × 3 slices = 150k small allocations) this dominates
// allocation profiles ahead of pdfcpu's content-stream decompression.
//
// A RunArena holds three growable slabs and hands out 3-arg sub-slices
// (s[start:end:end] — capped so a downstream append cannot clobber the
// next run's data). RunArenas are pooled via sync.Pool so peak alloc is
// the high-water mark of a single page rather than the cumulative
// sum across pages.
//
// Lifetime contract:
//
//   - A run's Glyphs / CharBounds / CharFlags slices are valid only
//     until ReleaseRunArena is called on the arena that produced them.
//   - chunk.Build owns the arena lifetime: it acquires one per page
//     in Phase A (text extraction) and releases them at end of Build,
//     after the cluster + classify + merge passes have consumed
//     everything they need from the slice contents (the produced
//     Chunks copy field-by-field into pure-Go strings + Boxes;
//     they do not retain references to per-glyph slices).
//   - Tests that construct TextRun literals manually own their own
//     slice memory; they do not need an arena.

package text

// RunArena holds bump-allocated slabs backing a page's text runs.
// Reset to len 0 between uses; cap retained so the next caller starts
// hot. Document.Chunks reuses one arena across all pages of one
// extraction pass; Reset clears the slabs without dropping cap so
// successive Chunks calls on the same Document amortize the slab
// allocation.
type RunArena struct {
	glyphs []uint16
	bounds []Rect
	flags  []uint8
}

// NewRunArena returns an empty arena. The slabs grow on demand via
// reserveGlyphs / reserveBounds / reserveFlags. Use
// NewRunArenaForPages when the caller knows the page count up front
// — pre-sizing the slabs avoids the doubling-grow loop that
// otherwise allocates ~2× the final byte count per extraction pass.
func NewRunArena() *RunArena {
	return &RunArena{}
}

// runsPerPageHint is the per-page glyph-count heuristic used by
// NewRunArenaForPages. Body-text-dense PDFs (DDIA, RFC text) carry
// up to ~2500 individual glyphs per page including marks-content
// metadata. Sized at the upper end of the observed range so most
// real-world docs skip the grow loop entirely; the slight
// over-allocation on small docs is one alloc instead of N grows.
const runsPerPageHint = 2500

// NewRunArenaForPages pre-allocates slab capacity based on a per-page
// glyph hint scaled by pageCount. For multi-page documents this skips
// the grow loop entirely; tiny documents pay a small over-allocation
// (one alloc of 1000 entries × element size) which is still cheaper
// than a grow chain.
func NewRunArenaForPages(pageCount int) *RunArena {
	if pageCount <= 0 {
		return NewRunArena()
	}
	estimated := pageCount * runsPerPageHint
	return &RunArena{
		glyphs: make([]uint16, 0, estimated),
		bounds: make([]Rect, 0, estimated),
		flags:  make([]uint8, 0, estimated),
	}
}

// ResetRunArena clears the arena's slabs (len → 0, cap retained) so
// the same arena can back the next extraction pass without re-growing.
// Nil arenas are tolerated as no-ops.
func ResetRunArena(a *RunArena) {
	if a == nil {
		return
	}
	a.glyphs = a.glyphs[:0]
	a.bounds = a.bounds[:0]
	a.flags = a.flags[:0]
}

// reserveGlyphs ensures cap(a.glyphs) - len(a.glyphs) >= n and returns
// the index at which the next n uint16s will live. Caller appends to
// a.glyphs and slices [start:start+n:start+n] (3-arg cap-bounded).
func (a *RunArena) reserveGlyphs(n int) int {
	start := len(a.glyphs)
	needed := start + n
	if needed > cap(a.glyphs) {
		newCap := max(max(cap(a.glyphs)*2, needed), 64)
		s := make([]uint16, start, newCap)
		copy(s, a.glyphs)
		a.glyphs = s
	}
	return start
}

// reserveBounds is reserveGlyphs's []Rect twin.
func (a *RunArena) reserveBounds(n int) int {
	start := len(a.bounds)
	needed := start + n
	if needed > cap(a.bounds) {
		newCap := max(max(cap(a.bounds)*2, needed), 32)
		s := make([]Rect, start, newCap)
		copy(s, a.bounds)
		a.bounds = s
	}
	return start
}

// reserveFlags is reserveGlyphs's []uint8 twin.
func (a *RunArena) reserveFlags(n int) int {
	start := len(a.flags)
	needed := start + n
	if needed > cap(a.flags) {
		newCap := max(max(cap(a.flags)*2, needed), 64)
		s := make([]uint8, start, newCap)
		copy(s, a.flags)
		a.flags = s
	}
	return start
}
