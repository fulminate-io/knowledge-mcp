package text

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
)

// emitTj turns a single literal-string operand into one TextRun. The
// glyph-byte semantics (1-byte/glyph for simple fonts, 2-byte/glyph
// for Type 0 / CID) are decided by the resolved font's Subtype.
// FontKey/FontName follow the locked semantics: FontKey is the
// resource-key from the most recent Tf operand (e.g. "F1"); FontName
// is the BaseFont (e.g. "Helvetica") read from the resolved font dict.
func (w *walker) emitTj(s []byte) {
	if len(s) == 0 {
		return // empty Tj — skip per Phase 8 contract
	}
	if w.state.suppressEmits {
		return
	}
	if !w.opts.IncludeInvisible && w.state.renderMode == 3 {
		return
	}

	glyphs := w.bytesToGlyphs(s)
	width, extents := w.advanceForString(s, glyphs)
	w.appendRun(glyphs, width, extents)
	// Advance the text matrix by the run's width (in text-space).
	w.advanceTm(width)
}

// emitTJ collapses a TJ array into a single TextRun. Numeric kerning
// adjustments contribute negatively (PDF convention §9.4.3): a
// positive number moves the baseline LEFT (subtract from cumulative
// advance, scaled by fontSize/1000).
func (w *walker) emitTJ(arr []operand) {
	if w.state.suppressEmits {
		return
	}
	if !w.opts.IncludeInvisible && w.state.renderMode == 3 {
		return
	}

	var glyphs []uint16
	var extents []glyphExtent
	width := 0.0
	for _, o := range arr {
		switch o.kind {
		case tokString, tokHexString:
			if len(o.s) == 0 {
				continue
			}
			subGlyphs := w.bytesToGlyphs(o.s)
			subWidth, subExt := w.advanceForString(o.s, subGlyphs)
			// Re-base each sub-string's per-glyph extents onto the
			// running TJ width so CharBounds tracks emitted glyphs
			// across kerning offsets. Kerning shifts width but does not
			// emit a glyph — bounds slice tracks emitted glyphs only.
			base := width
			for _, e := range subExt {
				extents = append(extents, glyphExtent{
					left:  base + e.left,
					right: base + e.right,
				})
			}
			glyphs = append(glyphs, subGlyphs...)
			width += subWidth
		case tokInt, tokFloat:
			// Per-mille kerning: subtract (n / 1000) * fontSize.
			width -= (o.f / 1000.0) * w.state.fontSize * w.state.horizScale
		}
	}
	if len(glyphs) == 0 {
		return
	}
	w.appendRun(glyphs, width, extents)
	w.advanceTm(width)
}

// bytesToGlyphs converts the literal-string bytes into the raw
// glyph-id sequence stored on TextRun.Glyphs. For simple fonts
// (Type1/TrueType/Type3/MMType1) PDF uses 1 byte = 1 glyph code;
// for Type 0 / composite fonts the canonical mapping is 2 bytes /
// glyph (CID-keyed). T2 makes a best-effort guess from
// state.font.Subtype and falls back to 1 byte / glyph when no font
// has been resolved.
func (w *walker) bytesToGlyphs(s []byte) []uint16 {
	twoByte := w.state.font != nil && w.state.font.Subtype == "Type0"

	if !twoByte {
		out := make([]uint16, len(s))
		for i, b := range s {
			out[i] = uint16(b)
		}
		return out
	}
	// Two bytes per glyph; pad the trailing odd byte with zero so
	// we don't drop a glyph silently.
	out := make([]uint16, 0, (len(s)+1)/2)
	for i := 0; i+1 < len(s); i += 2 {
		out = append(out, uint16(s[i])<<8|uint16(s[i+1]))
	}
	if len(s)%2 == 1 {
		out = append(out, uint16(s[len(s)-1])<<8)
	}
	return out
}

// advanceForString computes the run's advance width and the per-glyph
// extent slice (left/right cumulative advance per emitted glyph, with
// horizScale folded in — parallel to glyphs). The T3 ladder per PDF
// 32000-1:2008 §9.6.2.1 + §9.7.4.3 + §9.8.2 + §9.6.2.2:
//
//	Rung 1: /Widths array (when /FirstChar..LastChar covers the code)
//	Rung 2: FontDescriptor /MissingWidth (when /Widths absent)
//	Rung 3: Standard 14 hardcoded widths (Adobe AFM data via the
//	        single-source font/standard14_widths.dat per T3-2)
//	Rung 4: half-em placeholder + slog.Warn (rare; non-Standard-14
//	        fonts that omit /Widths)
//
// char-spacing and word-spacing are added per glyph per §9.4.4. The
// horizontal scale (Tz) is folded into the result; rise (Ts) does
// NOT affect width.
//
// The returned slice has len == len(glyphs); element i is the
// (left, right) extent of glyph i in user-space units. Tc / Tw
// contribute to the running cumulative advance AFTER the glyph (so
// they appear as a gap between glyph i's right and glyph i+1's left)
// rather than widening the glyph's own extent.
func (w *walker) advanceForString(raw []byte, glyphs []uint16) (float64, []glyphExtent) {
	width := 0.0
	extents := make([]glyphExtent, 0, len(glyphs))
	hs := w.state.horizScale
	for _, g := range glyphs {
		left := width
		width += w.glyphAdvance(uint32(g))
		right := width
		extents = append(extents, glyphExtent{left: left * hs, right: right * hs})
		width += w.state.charSpacing
		// Word-spacing adds to the literal-space glyph (0x20) per
		// §9.4.4 — only for simple fonts; Type 0 ignores Tw.
		if g == 0x20 && (w.state.font == nil || w.state.font.Subtype != "Type0") {
			width += w.state.wordSpacing
		}
	}
	_ = raw // unused parameter retained for API symmetry with future ToUnicode-aware callers
	return width * hs, extents
}

// glyphAdvance returns the user-space advance for one glyph code.
// Implements the 4-rung width-resolution ladder. fontSize-scaled.
func (w *walker) glyphAdvance(code uint32) float64 {
	f := w.state.font
	if f == nil {
		// No font set yet — half-em without warning. BT before first
		// Tf is malformed but rare and we don't want to spam.
		return w.state.fontSize * 0.5
	}
	// Rung 1: /Widths array hit.
	if len(f.Widths) > 0 {
		idx := int(code) - f.FirstChar
		if idx >= 0 && idx < len(f.Widths) {
			return float64(f.Widths[idx]) / 1000.0 * w.state.fontSize
		}
	}
	// Rung 2: /MissingWidth fallback.
	if f.MissingWidth != 0 {
		return float64(f.MissingWidth) / 1000.0 * w.state.fontSize
	}
	// Rung 3: Standard 14 hardcoded widths (font/standard14_widths.dat).
	if w14, ok := font.Standard14WidthForCode(f.BaseFont, code); ok {
		return float64(w14) / 1000.0 * w.state.fontSize
	}
	// Rung 4: half-em placeholder + once-per-BaseFont warning.
	if !w.widthFallbackLogged[f.BaseFont] {
		if w.widthFallbackLogged == nil {
			w.widthFallbackLogged = map[string]bool{}
		}
		w.widthFallbackLogged[f.BaseFont] = true
		slog.Warn("pdf/text: glyph advance falls back to half-em placeholder",
			"page", w.state.pageIndex, "fontKey", w.state.fontKey,
			"baseFont", f.BaseFont, "glyph", code)
	}
	return w.state.fontSize * 0.5
}

// appendRun records a TextRun with the current state's font and
// position. emit() is intentionally minimal — Phase 8 will gate it
// with renderMode/Type3 checks; Phase 7 will populate MCID. T2 sets
// MCID to the top of the marked-content stack (0 when empty).
//
// extents carries the per-glyph (left, right) cumulative advance pair
// for each emitted glyph (parallel to glyphs); appendRun maps each
// glyph's text-space (left, bottom)/(right, top) corners through the
// same combined Tm*CTM transform that produced the run-level (X, Y)
// origin, taking per-axis min/max so rotated text yields an
// axis-aligned user-space rect.
func (w *walker) appendRun(glyphs []uint16, width float64, extents []glyphExtent) {
	// User-space position: text-space (0, rise) transformed via
	// Trm = Tm × CTM (PDF 32000-1:2008 §9.4.4). Order matters when
	// the page-level CTM is non-identity (e.g. Antenna House and
	// other producers emit a Y-flip + translate to position the
	// origin at the top-left corner).
	combined := w.state.tm.mul(w.state.ctm)
	x, y := combined.transformPoint(0, w.state.rise)

	mcid := 0
	if n := len(w.state.mcidStack); n > 0 {
		mcid = w.state.mcidStack[n-1]
	}

	run := TextRun{
		Glyphs:  glyphs,
		X:       x,
		Y:       y,
		Width:   width,
		Height:  w.state.fontSize,
		FontKey: w.state.fontKey,
		Size:    w.state.fontSize,
		MCID:    mcid,
	}
	if w.state.font != nil {
		run.FontName = w.state.font.BaseFont
		run.Mono = w.state.font.Mono
		run.Bold = w.state.font.Bold
		run.Italic = w.state.font.Italic
	}
	// Inside a Form XObject with its own /Resources, capture the
	// top-of-stack Resources dict on the run so font.Decode can resolve
	// fontKey against it rather than the page-level dict. Mirrors the
	// stack-depth guard walker.resolveFont uses (content_stream.go:254).
	if n := len(w.formResourcesStack); n > 0 {
		run.formResources = w.formResourcesStack[n-1]
	}
	// Glyphs slab: copy the (possibly temp) glyphs into the arena so
	// run.Glyphs becomes a 3-arg-capped slice into the per-page arena
	// rather than a per-run heap allocation.
	if len(glyphs) > 0 && w.arena != nil {
		gs := w.arena.reserveGlyphs(len(glyphs))
		w.arena.glyphs = append(w.arena.glyphs, glyphs...)
		run.Glyphs = w.arena.glyphs[gs : gs+len(glyphs) : gs+len(glyphs)]
	}

	// CharBounds slab: write the per-glyph rects directly into the
	// arena. computeCharBoundsInto writes; we slice the result.
	if len(extents) == len(glyphs) && len(glyphs) > 0 && w.arena != nil {
		bs := w.arena.reserveBounds(len(extents))
		appendCharBounds(&w.arena.bounds, combined, w.state.rise, w.state.fontSize, extents)
		run.CharBounds = w.arena.bounds[bs : bs+len(extents) : bs+len(extents)]
	} else if len(extents) == len(glyphs) && len(glyphs) > 0 {
		run.CharBounds = computeCharBounds(combined, w.state.rise, w.state.fontSize, extents)
	}

	// CharFlags slab. Same arena trick: append zeroes (no flags) or
	// markedContent uniformly when the BMC/BDC stack is non-empty.
	// Public API contract: every run's CharFlags is parallel to its
	// Glyphs (zero bytes when no flag is set), so we always allocate
	// when there are glyphs.
	if len(glyphs) > 0 && w.arena != nil {
		fs := w.arena.reserveFlags(len(glyphs))
		var flag uint8
		if len(w.state.mcidStack) > 0 {
			flag = CharFlagMarkedContent
		}
		for range glyphs {
			w.arena.flags = append(w.arena.flags, flag)
		}
		run.CharFlags = w.arena.flags[fs : fs+len(glyphs) : fs+len(glyphs)]
	} else if len(glyphs) > 0 {
		run.CharFlags = make([]uint8, len(glyphs))
		if len(w.state.mcidStack) > 0 {
			for i := range run.CharFlags {
				run.CharFlags[i] |= CharFlagMarkedContent
			}
		}
	}
	w.runs = append(w.runs, run)
}

// advanceTm advances the text matrix by `width` user-space units in
// text-space. Per PDF 32000-1:2008 §9.4.4, the text matrix is
// updated by (width, 0) post-multiplied: tm' = T(width, 0) * tm.
// width is already in user-space points (advanceForString folds
// horizScale in).
func (w *walker) advanceTm(width float64) {
	w.state.tm = (matrix{a: 1, d: 1, e: width, f: 0}).mul(w.state.tm)
}
