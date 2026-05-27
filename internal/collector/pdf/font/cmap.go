package font

import (
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// /ToUnicode CMaps are a constrained subset of PostScript per Adobe
// Tech Note #5014. The parser handles begincmap/endcmap, beginbfchar/
// endbfchar, beginbfrange/endbfrange, and the notdef* analogs. The
// `usecmap` directive is silently rejected (per T2-4 — the named
// CMap could trigger arbitrary loader behavior).
//
// Bounded inputs defend against malicious CMaps amplifying memory:
//   - maxBfRangeSpan: largest single bfrange (hi - lo + 1)
//   - maxHexTokenBytes: largest single <...> hex token
//   - maxDirectives: total bfchar+bfrange+notdef* entries per CMap
//   - maxCMapBytes: total input size cap (returns ErrCMapTooLarge)
const (
	maxBfRangeSpan   = 0x110000 // unicode.MaxRune+1; nothing larger maps to real Unicode
	maxHexTokenBytes = 1 << 20  // 1 MiB
	maxDirectives    = 100_000  // total parsed entries across all directive blocks
	maxCMapBytes     = 16 << 20 // 16 MiB total input size
	bfRangeWarn      = "pdf/font: cmap: bfrange span exceeds maxBfRangeSpan"
	hexTokenWarn     = "pdf/font: cmap: hex string exceeds maxHexTokenBytes"
	directiveWarn    = "pdf/font: cmap: directive count exceeded maxDirectives"
)

// ErrCMapTooLarge is returned by parseCMap when the input bytes
// exceed maxCMapBytes. Loud-fail rather than silent truncation.
var ErrCMapTooLarge = errors.New("font: cmap input exceeds maxCMapBytes")

// cmap is the parsed /ToUnicode mapping. Lookup priority during
// decode: bfchars (exact) > bfranges (range hit) > notdefchars >
// notdefranges > (nil, false).
type cmap struct {
	bfchars      map[uint32][]rune
	bfranges     []bfrange
	notdefchars  map[uint32][]rune
	notdefranges []bfrange
}

// bfrange represents one beginbfrange/endbfrange entry.
//
// When `sequential` is true (hex-target form), targets has len==1;
// the entry decodes via base + (code - lo) per Adobe Tech Note #5014.
//
// When `sequential` is false (array-target form), targets has length
// hi-lo+1; the entry decodes via targets[code - lo].
type bfrange struct {
	lo, hi     uint32
	targets    [][]rune
	sequential bool
}

// parseCMapCalls counts parseCMap invocations for the document-scope
// caching regression test (Phase 9). Package-internal; export_test.go
// exposes a getter and reset helper for the resolver_test.go suite.
var parseCMapCalls atomic.Uint64

// parseCMap parses a /ToUnicode CMap from src and returns the
// resulting (cmap, error). Malformed individual directives are logged
// and skipped; only top-level errors (oversized input) surface as
// errors. Empty input returns an empty cmap, not an error.
func parseCMap(src []byte) (*cmap, error) {
	parseCMapCalls.Add(1)
	if len(src) > maxCMapBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrCMapTooLarge, len(src), maxCMapBytes)
	}
	c := &cmap{bfchars: map[uint32][]rune{}, notdefchars: map[uint32][]rune{}}
	tk := newCMapTokenizer(src)
	directives := 0
	for {
		tok, ok := tk.next()
		if !ok {
			break
		}
		if tok.kind != tkKeyword {
			continue
		}
		switch tok.text {
		case "beginbfchar":
			directives = parseBfChar(tk, c.bfchars, directives)
		case "beginnotdefchar":
			directives = parseBfChar(tk, c.notdefchars, directives)
		case "beginbfrange":
			directives = parseBfRange(tk, &c.bfranges, directives)
		case "beginnotdefrange":
			directives = parseBfRange(tk, &c.notdefranges, directives)
		case "usecmap":
			// silent skip per T2-4 — see file-level note.
		}
		if directives >= maxDirectives {
			slog.Warn(directiveWarn, "max", maxDirectives)
			break
		}
	}
	return c, nil
}

// parseBfChar consumes pairs of <hexSrc> <hexTarget> until "endbfchar"
// (or "endnotdefchar") is encountered. Returns updated directive
// count. Pairs whose target exceeds maxHexTokenBytes are skipped with
// a slog warning; the enclosing block continues to the matching end*.
//
// Enforces the maxDirectives cap mid-block: once `count` reaches the
// cap, further entries within this block are dropped (the block still
// drains to its endbfchar/endnotdefchar token so the outer loop
// resumes at a clean position).
func parseBfChar(tk *cmapTokenizer, dst map[uint32][]rune, count int) int {
	capped := false
	for {
		src, ok := tk.next()
		if !ok || (src.kind == tkKeyword && (src.text == "endbfchar" || src.text == "endnotdefchar")) {
			return count
		}
		if src.kind != tkHex {
			continue
		}
		tgt, ok := tk.next()
		if !ok {
			return count
		}
		if tgt.kind != tkHex {
			continue
		}
		if count >= maxDirectives {
			if !capped {
				slog.Warn(directiveWarn, "max", maxDirectives)
				capped = true
			}
			continue
		}
		if len(tgt.bytes) > maxHexTokenBytes {
			slog.Warn(hexTokenWarn, "len", len(tgt.bytes))
			continue
		}
		code := bytesToCode(src.bytes)
		runes := utf16BytesToRunes(tgt.bytes)
		if len(runes) > 0 {
			dst[code] = runes
			count++
		}
	}
}

// parseBfRange consumes triplets of <hexLo> <hexHi> (<hexBase> | [array])
// until "endbfrange" (or "endnotdefrange"). Returns updated count.
// Out-of-range spans are skipped with a slog warning.
func parseBfRange(tk *cmapTokenizer, dst *[]bfrange, count int) int {
	for {
		lo, ok := tk.next()
		if !ok || (lo.kind == tkKeyword && (lo.text == "endbfrange" || lo.text == "endnotdefrange")) {
			return count
		}
		if lo.kind != tkHex {
			continue
		}
		hi, ok := tk.next()
		if !ok {
			return count
		}
		if hi.kind != tkHex {
			continue
		}
		loCode, hiCode := bytesToCode(lo.bytes), bytesToCode(hi.bytes)
		if hiCode < loCode {
			loCode, hiCode = hiCode, loCode
		}
		span := uint64(hiCode-loCode) + 1
		if span > maxBfRangeSpan {
			slog.Warn(bfRangeWarn, "lo", loCode, "hi", hiCode, "span", span)
			// Drain to next directive without recording anything.
			drainBfRangeTarget(tk)
			continue
		}
		t, ok := tk.next()
		if !ok {
			return count
		}
		switch t.kind {
		case tkHex:
			if len(t.bytes) > maxHexTokenBytes {
				slog.Warn(hexTokenWarn, "len", len(t.bytes))
				continue
			}
			runes := utf16BytesToRunes(t.bytes)
			*dst = append(*dst, bfrange{lo: loCode, hi: hiCode, targets: [][]rune{runes}, sequential: true})
			count++
		case tkArrayStart:
			targets := readArrayTargets(tk)
			*dst = append(*dst, bfrange{lo: loCode, hi: hiCode, targets: targets, sequential: false})
			count++
		}
	}
}

// drainBfRangeTarget consumes the next token (hex or array) so the
// outer loop's tokenizer is back at a hexLo/end* boundary.
func drainBfRangeTarget(tk *cmapTokenizer) {
	t, ok := tk.next()
	if !ok {
		return
	}
	if t.kind == tkArrayStart {
		// drain to ']'
		for {
			x, ok := tk.next()
			if !ok || x.kind == tkArrayEnd {
				return
			}
		}
	}
}

// readArrayTargets consumes hex tokens until ']' and returns each
// hex's UTF-16BE-decoded rune slice.
func readArrayTargets(tk *cmapTokenizer) [][]rune {
	var out [][]rune
	for {
		t, ok := tk.next()
		if !ok || t.kind == tkArrayEnd {
			return out
		}
		if t.kind != tkHex {
			continue
		}
		if len(t.bytes) > maxHexTokenBytes {
			slog.Warn(hexTokenWarn, "len", len(t.bytes))
			continue
		}
		out = append(out, utf16BytesToRunes(t.bytes))
	}
}
