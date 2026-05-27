package text

// Operand-stack pop helpers. Each helper inspects the stack height
// and the top-of-stack operand kind(s); on shape mismatch or
// underflow it logs via walker.underflow (slog.Warn) and returns
// the zero value plus ok=false. The dispatcher then skips the
// operator without consuming any operands. The caller (walker.run)
// clears the operand stack after every dispatch — so even a
// successful pop's residue is wiped before the next operator.

// popFloat pops one numeric operand. Returns (0, false) on underflow
// or non-numeric top.
func (w *walker) popFloat(op string) (float64, bool) {
	if len(w.stack) < 1 {
		w.underflow(op, 1, 0)
		return 0, false
	}
	o := w.stack[len(w.stack)-1]
	if o.kind != tokInt && o.kind != tokFloat {
		w.underflow(op, 1, 0)
		return 0, false
	}
	w.stack = w.stack[:len(w.stack)-1]
	return o.f, true
}

// popPair pops two numeric operands. Returns (0, 0, false) on
// underflow.
func (w *walker) popPair(op string) (float64, float64, bool) {
	if len(w.stack) < 2 {
		w.underflow(op, 2, len(w.stack))
		return 0, 0, false
	}
	tx := w.stack[len(w.stack)-2]
	ty := w.stack[len(w.stack)-1]
	if !numeric(tx) || !numeric(ty) {
		w.underflow(op, 2, len(w.stack))
		return 0, 0, false
	}
	w.stack = w.stack[:len(w.stack)-2]
	return tx.f, ty.f, true
}

// popSix pops six numeric operands.
func (w *walker) popSix(op string) (a, b, c, d, e, f float64, ok bool) {
	if len(w.stack) < 6 {
		w.underflow(op, 6, len(w.stack))
		return
	}
	t := w.stack[len(w.stack)-6:]
	for _, x := range t {
		if !numeric(x) {
			w.underflow(op, 6, len(w.stack))
			return
		}
	}
	a = t[0].f
	b = t[1].f
	c = t[2].f
	d = t[3].f
	e = t[4].f
	f = t[5].f
	w.stack = w.stack[:len(w.stack)-6]
	ok = true
	return
}

// popString pops a single string-like operand (tokString or
// tokHexString).
func (w *walker) popString(op string) ([]byte, bool) {
	if len(w.stack) < 1 {
		w.underflow(op, 1, 0)
		return nil, false
	}
	o := w.stack[len(w.stack)-1]
	if o.kind != tokString && o.kind != tokHexString {
		w.underflow(op, 1, 0)
		return nil, false
	}
	w.stack = w.stack[:len(w.stack)-1]
	return o.s, true
}

// popArray pops a single array operand.
func (w *walker) popArray(op string) ([]operand, bool) {
	if len(w.stack) < 1 {
		w.underflow(op, 1, 0)
		return nil, false
	}
	o := w.stack[len(w.stack)-1]
	if o.kind != tokArrayStart {
		w.underflow(op, 1, 0)
		return nil, false
	}
	w.stack = w.stack[:len(w.stack)-1]
	return o.arr, true
}

// popName pops a single name operand. Used by BMC/BDC/Do.
func (w *walker) popName(op string) ([]byte, bool) {
	if len(w.stack) < 1 {
		w.underflow(op, 1, 0)
		return nil, false
	}
	o := w.stack[len(w.stack)-1]
	if o.kind != tokName {
		w.underflow(op, 1, 0)
		return nil, false
	}
	w.stack = w.stack[:len(w.stack)-1]
	return o.s, true
}

// popTf reads `key size Tf`.
func (w *walker) popTf() (string, float64, bool) {
	if len(w.stack) < 2 {
		w.underflow("Tf", 2, len(w.stack))
		return "", 0, false
	}
	name := w.stack[len(w.stack)-2]
	size := w.stack[len(w.stack)-1]
	if name.kind != tokName || !numeric(size) {
		w.underflow("Tf", 2, len(w.stack))
		return "", 0, false
	}
	w.stack = w.stack[:len(w.stack)-2]
	return string(name.s), size.f, true
}

// popQuoteOperands reads `aw ac (string) "`.
func (w *walker) popQuoteOperands() (s []byte, cs, ws float64, ok bool) {
	if len(w.stack) < 3 {
		w.underflow("\"", 3, len(w.stack))
		return
	}
	aw := w.stack[len(w.stack)-3]
	ac := w.stack[len(w.stack)-2]
	str := w.stack[len(w.stack)-1]
	if !numeric(aw) || !numeric(ac) || (str.kind != tokString && str.kind != tokHexString) {
		w.underflow("\"", 3, len(w.stack))
		return
	}
	w.stack = w.stack[:len(w.stack)-3]
	return str.s, ac.f, aw.f, true
}

// numeric reports whether o is an int or float operand.
func numeric(o operand) bool {
	return o.kind == tokInt || o.kind == tokFloat
}
