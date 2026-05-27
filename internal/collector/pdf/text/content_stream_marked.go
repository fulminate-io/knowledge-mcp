package text

import (
	"log/slog"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// mcidDepthCap is the cap on marked-content nesting depth. Defends
// against malformed PDFs that would otherwise grow the stack
// unboundedly. Adobe Reader's reported limit is 32; pdfcpu's own
// MAX_RECURSE_LEVEL is 50. We pick 32 to match the more conservative
// reader behavior. The 33rd push is logged-and-dropped.
const mcidDepthCap = 32

// dispatchBMC handles the BMC operator (begin marked-content with no
// property dict). PDF 32000-1:2008 §14.6.2: BMC takes one name
// operand (the tag, e.g. /Span). Since there is no property dict,
// the current MCID is unchanged — we propagate the parent's MCID by
// pushing the current top of the stack (0 if empty).
func (w *walker) dispatchBMC() {
	// Consume the tag name; we don't store it — Phase 9's BMC tests
	// just confirm the dispatcher accepts the operator without
	// crashing.
	if _, ok := w.popName("BMC"); !ok {
		return
	}
	parent := 0
	if n := len(w.state.mcidStack); n > 0 {
		parent = w.state.mcidStack[n-1]
	}
	w.pushMCID(parent)
}

// dispatchBDC handles the BDC operator (begin marked-content with
// property dict). PDF 32000-1:2008 §14.6.2: BDC takes a tag name
// followed by either an inline dict OR a name referencing a property
// list in the page's /Properties resource subdict.
//
// T2 supports only the inline-dict form. The name-reference form
// logs a warning and treats MCID as 0 (T6's structtree pipeline will
// extend this when real-corpus tagged PDFs need it).
//
// Operand-stack layout: [..., tag, props].
func (w *walker) dispatchBDC() {
	if len(w.stack) < 2 {
		w.underflow("BDC", 2, len(w.stack))
		return
	}
	props := w.stack[len(w.stack)-1]
	tag := w.stack[len(w.stack)-2]
	if tag.kind != tokName {
		w.underflow("BDC", 2, len(w.stack))
		return
	}
	w.stack = w.stack[:len(w.stack)-2]

	mcid := 0
	switch props.kind {
	case tokDictStart:
		mcid = readMCID(props.arr)
	case tokName:
		slog.Warn("pdf/text: BDC properties given as name reference; "+
			"name-resolution deferred to T6, MCID treated as 0",
			"page", w.state.pageIndex, "tag", string(tag.s), "ref", string(props.s))
	default:
		slog.Warn("pdf/text: BDC properties operand has unexpected kind",
			"page", w.state.pageIndex, "tag", string(tag.s))
	}
	w.pushMCID(mcid)
}

// dispatchEMC pops the marked-content stack. On underflow (more EMCs
// than BMCs/BDCs, a malformed PDF) the operator is a no-op with a
// logged warning.
func (w *walker) dispatchEMC() {
	w.popMCID()
}

// pushMCID appends id to the marked-content stack. When the cap is
// reached, the push is logged and dropped — subsequent emits
// continue carrying the current top until a matching EMC appears.
func (w *walker) pushMCID(id int) {
	if len(w.state.mcidStack) >= mcidDepthCap {
		slog.Warn("pdf/text: marked-content stack at cap; push dropped",
			"page", w.state.pageIndex, "mcidDepthCap", mcidDepthCap, "dropped", id)
		return
	}
	w.state.mcidStack = append(w.state.mcidStack, id)
}

// popMCID removes the top entry from the marked-content stack. On
// empty stack the operation logs and is a no-op.
func (w *walker) popMCID() {
	n := len(w.state.mcidStack)
	if n == 0 {
		slog.Warn("pdf/text: EMC with empty marked-content stack",
			"page", w.state.pageIndex)
		return
	}
	w.state.mcidStack = w.state.mcidStack[:n-1]
}

// readMCID walks a flat (k, v, k, v, ...) operand slice as built by
// walker.readDict and returns the integer value of the /MCID entry,
// or 0 when absent or wrong-typed.
func readMCID(entries []operand) int {
	for i := 0; i+1 < len(entries); i += 2 {
		k := entries[i]
		v := entries[i+1]
		if k.kind != tokName || string(k.s) != "MCID" {
			continue
		}
		if v.kind == tokInt || v.kind == tokFloat {
			return int(v.f)
		}
	}
	return 0
}

// dispatchDo handles the Do operator (paint XObject). PDF
// 32000-1:2008 §8.10.1: Do takes one name operand referring to an
// /XObject resource. Form XObjects are recursed into — their content
// stream is walked with the surrounding graphics state preserved
// across the recursion (q before, Q after) and the Form's /Matrix
// applied as a CTM concatenation. The Form's /Resources, when
// present, is pushed onto formResourcesStack so font keys inside
// the Form resolve against the Form's own font subdict rather than
// the page's. Image XObjects and unknown subtypes are silently
// skipped. Recursion depth is capped at maxFormDepth.
func (w *walker) dispatchDo() {
	name, ok := w.popName("Do")
	if !ok {
		return
	}
	if w.page == nil {
		// Walker driven directly by tests via extractFromBytes — no
		// page handle to resolve XObjects against. Treat as unknown
		// kind and skip silently.
		return
	}
	kind, err := w.page.XObjectKind(string(name))
	if err != nil {
		slog.Warn("pdf/text: Do XObject lookup error",
			"page", w.state.pageIndex, "name", string(name), "err", err.Error())
		return
	}
	switch kind {
	case internalpdf.XObjectForm:
		w.recurseForm(string(name))
	case internalpdf.XObjectImage, "":
		// Silent skip — image XObjects and unknown subtypes both
		// produce zero TextRuns without log noise.
	default:
		// Some other subtype (PostScript, PS — rare). No log noise.
	}
}

// recurseForm fetches the named Form XObject and walks its content
// stream as if inlined at the Do site. The PDF spec sandwiches Form
// invocation between an implicit `q` and `Q` (PDF 32000-1:2008 §8.10),
// so the surrounding graphics state is restored after the Form
// returns. The Form's /Matrix is applied as a CTM concatenation; its
// /Resources, when present, is pushed onto formResourcesStack so
// font lookups inside the Form bind to the Form's font dict.
func (w *walker) recurseForm(name string) {
	if w.formDepth >= maxFormDepth {
		slog.Warn("pdf/text: Form XObject recursion depth exceeded; skipping",
			"page", w.state.pageIndex, "name", name, "depth", w.formDepth)
		return
	}
	fx, err := w.page.FormXObject(name)
	if err != nil {
		slog.Warn("pdf/text: Form XObject fetch error",
			"page", w.state.pageIndex, "name", name, "err", err.Error())
		return
	}
	if fx == nil {
		return
	}
	// Cycle detection: skip when this Form's underlying object is
	// already active on the recursion stack. ObjectKey is empty when
	// the Form was reached via an inline (non-indirect) entry; inline
	// Forms can't reference themselves, so the empty-key path bypasses
	// the check safely.
	if fx.ObjectKey != "" && w.formStack[fx.ObjectKey] > 0 {
		slog.Warn("pdf/text: Form XObject cycle detected; skipping",
			"page", w.state.pageIndex, "name", name, "key", fx.ObjectKey)
		return
	}

	w.state.applyQ()
	if fx.HasMatrix {
		w.state.applyCm(fx.Matrix[0], fx.Matrix[1], fx.Matrix[2], fx.Matrix[3], fx.Matrix[4], fx.Matrix[5])
	}
	pushedResources := false
	if fx.Resources != nil {
		w.formResourcesStack = append(w.formResourcesStack, fx.Resources)
		pushedResources = true
	}
	w.formDepth++
	if fx.ObjectKey != "" {
		w.formStack[fx.ObjectKey]++
	}

	if err := w.run(fx.Bytes); err != nil {
		slog.Warn("pdf/text: Form XObject content-stream parse error",
			"page", w.state.pageIndex, "name", name, "err", err.Error())
	}

	if fx.ObjectKey != "" {
		w.formStack[fx.ObjectKey]--
	}
	w.formDepth--
	if pushedResources {
		w.formResourcesStack = w.formResourcesStack[:len(w.formResourcesStack)-1]
	}
	w.state.applyBigQ()
}
