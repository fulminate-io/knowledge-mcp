package text

import (
	"log/slog"
)

// dispatch is the per-operator switch. The sheer number of cases is
// the natural shape of the PDF content-stream grammar; flattening
// keeps every operator visible at a glance and avoids premature
// abstraction. Operators we don't model (path / color / image /
// shading) fall through to the default case and are silently
// ignored — the operand stack is cleared by the caller after every
// dispatch return.
//
// Operand-stack underflow contract: every pop helper checks the stack
// before accessing it; on insufficient operands, the helper returns
// false and dispatch logs+skips the operator without consuming any
// operands. The walker.run loop then clears the operand stack and
// proceeds with the next token. Mirrors Phase 4's applyBigQ contract
// and pdfminer-style robustness on real-corpus malformed PDFs.
func (w *walker) dispatch(op string) {
	switch op {
	case "BT":
		w.state.resetForBT()
		w.inText = true
	case "ET":
		w.inText = false
	case "Tf":
		key, size, ok := w.popTf()
		if !ok {
			return
		}
		if err := w.applyTfWithSuppression(key, size); err != nil {
			slog.Warn("pdf/text: applyTf error",
				"key", key, "err", err.Error(), "page", w.state.pageIndex)
		}
	case "Tc", "Tw", "Tz", "TL", "Tr", "Ts":
		w.dispatchTextState(op)
	case "Td", "TD", "Tm", "T*":
		w.dispatchPosition(op)
	case "Tj", "TJ", "'", "\"":
		w.dispatchShow(op)
	case "q":
		w.state.applyQ()
	case "Q":
		w.state.applyBigQ()
	case "cm":
		a, b, c, d, e, f, ok := w.popSix("cm")
		if !ok {
			return
		}
		w.state.applyCm(a, b, c, d, e, f)
	case "BMC":
		w.dispatchBMC()
	case "BDC":
		w.dispatchBDC()
	case "EMC":
		w.dispatchEMC()
	case "Do":
		w.dispatchDo()
	default:
		// Unmodelled operators (path / color / image / shading);
		// the operand stack will be cleared by the caller.
	}
}

// dispatchPosition handles the four text-positioning operators (Td, TD,
// Tm, T*). Extracted from dispatch to keep the main switch under the
// project's funlen threshold.
func (w *walker) dispatchPosition(op string) {
	switch op {
	case "Td":
		tx, ty, ok := w.popPair("Td")
		if !ok {
			return
		}
		w.state.applyTd(tx, ty)
	case "TD":
		tx, ty, ok := w.popPair("TD")
		if !ok {
			return
		}
		w.state.applyTD(tx, ty)
	case "Tm":
		a, b, c, d, e, f, ok := w.popSix("Tm")
		if !ok {
			return
		}
		w.state.applyTm(a, b, c, d, e, f)
	case "T*":
		w.state.applyTStar()
	}
}

// dispatchShow handles the four text-showing operators (Tj, TJ, ', ").
// Each pops its operand(s), updates state, and emits one TextRun.
// Extracted from dispatch to keep the main switch under funlen.
func (w *walker) dispatchShow(op string) {
	switch op {
	case "Tj":
		s, ok := w.popString("Tj")
		if !ok {
			return
		}
		w.emitTj(s)
	case "TJ":
		arr, ok := w.popArray("TJ")
		if !ok {
			return
		}
		w.emitTJ(arr)
	case "'":
		s, ok := w.popString("'")
		if !ok {
			return
		}
		w.state.applyTStar()
		w.emitTj(s)
	case "\"":
		s, cs, ws, ok := w.popQuoteOperands()
		if !ok {
			return
		}
		w.state.applyTw(ws)
		w.state.applyTc(cs)
		w.state.applyTStar()
		w.emitTj(s)
	}
}

// dispatchTextState handles the six single-float text-state operators
// (Tc, Tw, Tz, TL, Tr, Ts). Each pops one float and applies it to the
// corresponding state field. Extracted from dispatch to keep the main
// switch under the project's gocognit threshold.
func (w *walker) dispatchTextState(op string) {
	v, ok := w.popFloat(op)
	if !ok {
		return
	}
	switch op {
	case "Tc":
		w.state.applyTc(v)
	case "Tw":
		w.state.applyTw(v)
	case "Tz":
		w.state.applyTz(v)
	case "TL":
		w.state.applyTL(v)
	case "Tr":
		w.state.applyTr(int(v))
	case "Ts":
		w.state.applyTs(v)
	}
}

// applyTfWithSuppression wraps state.applyTf and additionally tracks
// Type3 fonts on this page. When applyTf resolves a font with
// Subtype == "Type3", we log once per (page, fontKey) and set
// state.suppressEmits so subsequent Tj/TJ emit nothing. Switching
// to a non-Type3 font clears suppressEmits.
func (w *walker) applyTfWithSuppression(key string, size float64) error {
	if err := w.state.applyTf(key, size, w.resolveFont); err != nil {
		return err
	}
	if w.state.font != nil && w.state.font.Subtype == "Type3" {
		if !w.type3Logged[key] {
			w.type3Logged[key] = true
			slog.Warn("pdf/text: Type 3 font encountered; runs suppressed",
				"page", w.state.pageIndex, "fontKey", key)
		}
		w.state.suppressEmits = true
		return nil
	}
	w.state.suppressEmits = false
	return nil
}

// Pop helpers live in content_stream_pop.go.
