package font

import "unicode/utf16"

// bytesToCode interprets a hex-decoded byte sequence as a big-endian
// uint32 code. Up to 4 bytes are honored; longer inputs are truncated
// to the high 4 bytes.
func bytesToCode(b []byte) uint32 {
	var v uint32
	n := min(len(b), 4)
	for i := range n {
		v = v<<8 | uint32(b[i])
	}
	return v
}

// utf16BytesToRunes interprets b as UTF-16BE (2 bytes per code unit)
// and returns the decoded rune slice. Surrogate pairs are joined.
// Odd trailing bytes are dropped silently.
func utf16BytesToRunes(b []byte) []rune {
	n := len(b) / 2
	if n == 0 {
		return nil
	}
	u16 := make([]uint16, n)
	for i := range n {
		u16[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return utf16.Decode(u16)
}

// decode looks up cid in the cmap. Lookup priority: bfchars (exact),
// bfranges (range hit), notdefchars, notdefranges. Returns (nil, false)
// when no entry matches.
func (c *cmap) decode(cid uint32) ([]rune, bool) {
	if rs, ok := c.bfchars[cid]; ok {
		return rs, true
	}
	if rs, ok := lookupRange(c.bfranges, cid); ok {
		return rs, true
	}
	if rs, ok := c.notdefchars[cid]; ok {
		return rs, true
	}
	if rs, ok := lookupRange(c.notdefranges, cid); ok {
		return rs, true
	}
	return nil, false
}

// decodeInto is the alloc-free hot path: looks up cid and writes the
// resulting runes directly to the supplied strings.Builder. Returns
// true when a mapping was found. Used by the per-glyph resolveGlyphs
// loop where the alternative would allocate a fresh []rune per
// lookup — for documents with millions of glyphs that allocation
// dominates the heap.
func (c *cmap) decodeInto(cid uint32, b stringWriter) bool {
	if rs, ok := c.bfchars[cid]; ok {
		for _, r := range rs {
			_, _ = b.WriteRune(r)
		}
		return true
	}
	if writeRangeInto(c.bfranges, cid, b) {
		return true
	}
	if rs, ok := c.notdefchars[cid]; ok {
		for _, r := range rs {
			_, _ = b.WriteRune(r)
		}
		return true
	}
	return writeRangeInto(c.notdefranges, cid, b)
}

// stringWriter is the minimum surface decodeInto needs from
// strings.Builder; declared as an interface so test fakes can target
// it without importing strings.Builder directly.
type stringWriter interface {
	WriteRune(r rune) (int, error)
}

// writeRangeInto is lookupRange's alloc-free twin: scans ranges and
// writes the matched runes directly to b.
func writeRangeInto(ranges []bfrange, cid uint32, b stringWriter) bool {
	for _, r := range ranges {
		if cid < r.lo || cid > r.hi {
			continue
		}
		if r.sequential {
			if len(r.targets) == 0 || len(r.targets[0]) == 0 {
				return false
			}
			base := r.targets[0]
			for i, rn := range base {
				if i == len(base)-1 {
					rn += rune(cid - r.lo)
				}
				_, _ = b.WriteRune(rn)
			}
			return true
		}
		idx := int(cid - r.lo)
		if idx < 0 || idx >= len(r.targets) {
			return false
		}
		for _, rn := range r.targets[idx] {
			_, _ = b.WriteRune(rn)
		}
		return true
	}
	return false
}

// lookupRange linearly scans ranges for a hit. CMaps typically have <50
// ranges per font; linear scan is correct and fast for this size.
func lookupRange(ranges []bfrange, cid uint32) ([]rune, bool) {
	for _, r := range ranges {
		if cid < r.lo || cid > r.hi {
			continue
		}
		if r.sequential {
			if len(r.targets) == 0 || len(r.targets[0]) == 0 {
				return nil, false
			}
			base := r.targets[0]
			out := make([]rune, len(base))
			copy(out, base)
			out[len(out)-1] += rune(cid - r.lo)
			return out, true
		}
		idx := int(cid - r.lo)
		if idx < 0 || idx >= len(r.targets) {
			return nil, false
		}
		return r.targets[idx], true
	}
	return nil, false
}
