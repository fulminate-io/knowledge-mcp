package text

import (
	"log/slog"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// state is the per-page text-rendering state machine described by
// PDF 32000-1:2008 §9.3. Each field corresponds to a value the PDF
// content-stream operator vocabulary mutates: Tf sets font/size, Tc
// sets character spacing, Tw word spacing, Tz horizontal scale, TL
// leading, Tr render mode, Ts rise. The text matrix Tm and
// text-line matrix Tlm carry the cursor between Tj operators; the
// CTM (current transformation matrix) is the graphics-state
// transform that q/Q save/restore and cm appends to.
//
// Simplification: q/Q (graphics-state push/pop) save and restore ONLY
// the CTM, not the full graphics state including text state. PDF
// 32000-1:2008 §8.4 + Table 51 strictly require saving text state on
// q. We mirror pdfminer-style behavior: text state is reset on every
// BT (per §9.4.1) and otherwise persists across q/Q boundaries. This
// is documented as a known divergence; if real-corpus PDFs mutate
// text state inside q/Q without a BT, T3+ revisits.
type state struct {
	// Text state — set by Tf / Tc / Tw / Tz / TL / Tr / Ts
	font        *internalpdf.ResolvedFont // resolved font (nil until first Tf); embeds *FontResource so existing field reads (Subtype, BaseFont, Mono/Bold/Italic) continue to work via Go field promotion.
	fontKey     string                    // resource name (e.g. "F1") — last Tf operand 1
	fontSize    float64                   // points — last Tf operand 2
	charSpacing float64                   // Tc — extra spacing after each glyph (user units)
	wordSpacing float64                   // Tw — extra spacing after each literal-space glyph
	horizScale  float64                   // Tz / 100 — horizontal scale factor (1.0 = 100%)
	leading     float64                   // TL — used by T* / TD / ' / "
	renderMode  int                       // Tr — 0=fill, 1=stroke, 2=fill+stroke, 3=invisible, 4-7=clip variants
	rise        float64                   // Ts — baseline shift in user space

	// Text-positioning state — Tm / Td / TD / T* mutate
	tm  matrix // text matrix
	tlm matrix // text-line matrix; reset on BT/Tm, advanced on T*/TD/Td

	// Graphics state — q / Q / cm
	ctm     matrix   // current transformation matrix
	gsStack []matrix // saved-CTM stack (q pushes, Q pops)

	// Marked content — Phase 7 wires BMC/BDC/EMC.
	mcidStack []int

	// Type 3 / Form XObject suppression — Phase 8 wires.
	suppressEmits bool

	// pageIndex is included in log warnings for diagnosability.
	pageIndex int
}

// newState returns a fresh state with the identity CTM, no font, and
// the PDF spec defaults for every other field. The caller (the walker)
// sets pageIndex before driving the content stream.
func newState() *state {
	return &state{
		horizScale: 1.0,
		tm:         identityMatrix,
		tlm:        identityMatrix,
		ctm:        identityMatrix,
	}
}

// resetForBT resets the text matrix and text-line matrix to identity
// per PDF 32000-1:2008 §9.4.1 ("BT begins a text object, initializing
// the text matrix Tm and the text-line matrix Tlm to the identity
// matrix"). Other text-state fields (font, sizes, render mode) are
// PRESERVED across BT/ET boundaries — only the cursor resets.
func (s *state) resetForBT() {
	s.tm = identityMatrix
	s.tlm = identityMatrix
}

// applyTf sets the font and size. resolveFont is the closure the
// walker supplies; state.go is decoupled from the page handle so unit
// tests can stub the resolver. resolveFont may return (nil, nil) when
// the resource name is unknown — the state still records fontKey/size
// so subsequent Tj/TJ operators emit TextRuns with empty FontName.
func (s *state) applyTf(key string, size float64, resolveFont func(string) (*internalpdf.ResolvedFont, error)) error {
	s.fontKey = key
	s.fontSize = size
	if resolveFont == nil {
		s.font = nil
		return nil
	}
	res, err := resolveFont(key)
	if err != nil {
		s.font = nil
		return err
	}
	s.font = res
	return nil
}

// applyTc sets the character-spacing (Tc operator).
func (s *state) applyTc(c float64) { s.charSpacing = c }

// applyTw sets the word-spacing (Tw operator).
func (s *state) applyTw(w float64) { s.wordSpacing = w }

// applyTz sets the horizontal scale. PDF supplies a percentage; we
// store the multiplicative factor (100 -> 1.0).
func (s *state) applyTz(scalePercent float64) { s.horizScale = scalePercent / 100.0 }

// applyTL sets the leading (TL operator).
func (s *state) applyTL(leading float64) { s.leading = leading }

// applyTr sets the render mode (Tr operator). Modes 0-2 fill/stroke,
// 3=invisible (Phase 8 suppresses emits when this is set), 4-7 add
// the rendered glyph to the clipping path.
func (s *state) applyTr(mode int) { s.renderMode = mode }

// applyTs sets the text rise (Ts operator).
func (s *state) applyTs(rise float64) { s.rise = rise }

// applyTd handles the Td operator: tlm := translate(tx, ty) * tlm,
// then tm := tlm. PDF 32000-1:2008 §9.4.2 defines Td as a translation
// of the text-line matrix; the text matrix is then reset to the new
// line matrix.
func (s *state) applyTd(tx, ty float64) {
	t := matrix{a: 1, d: 1, e: tx, f: ty}
	s.tlm = t.mul(s.tlm)
	s.tm = s.tlm
}

// applyTD handles TD: equivalent to (-ty) TL Td (set leading to -ty,
// then translate). PDF 32000-1:2008 §9.4.2.
func (s *state) applyTD(tx, ty float64) {
	s.leading = -ty
	s.applyTd(tx, ty)
}

// applyTm sets both Tm and Tlm to the supplied 6-element matrix.
// Per PDF 32000-1:2008 §9.4.2 Tm is an absolute set, not a multiply.
func (s *state) applyTm(a, b, c, d, e, f float64) {
	m := matrix{a: a, b: b, c: c, d: d, e: e, f: f}
	s.tm = m
	s.tlm = m
}

// applyTStar handles the T* operator (next line): equivalent to
// `0 -leading Td`. PDF 32000-1:2008 §9.4.2.
func (s *state) applyTStar() {
	s.applyTd(0, -s.leading)
}

// applyQ pushes the current CTM onto the graphics-state stack.
// Aligns with PDF q operator. Note we do NOT save text state — see
// the package-level note on the simplification.
func (s *state) applyQ() {
	s.gsStack = append(s.gsStack, s.ctm)
}

// applyBigQ pops the top CTM from the graphics-state stack. On empty
// stack (a malformed PDF with more Q than q), log a warning via
// log/slog and treat the operator as a no-op. The state stays usable;
// any subsequent operator continues against the current CTM.
func (s *state) applyBigQ() {
	n := len(s.gsStack)
	if n == 0 {
		slog.Warn("pdf/text: Q operator with empty graphics-state stack",
			"page", s.pageIndex)
		return
	}
	s.ctm = s.gsStack[n-1]
	s.gsStack = s.gsStack[:n-1]
}

// applyCm composes the supplied 6-element matrix onto the current
// CTM. Per PDF 32000-1:2008 §8.4.4 cm represents a transformation
// concatenation: newCTM = operandMatrix * oldCTM (the operand is
// applied first when transforming a point under the new CTM).
func (s *state) applyCm(a, b, c, d, e, f float64) {
	m := matrix{a: a, b: b, c: c, d: d, e: e, f: f}
	s.ctm = m.mul(s.ctm)
}
