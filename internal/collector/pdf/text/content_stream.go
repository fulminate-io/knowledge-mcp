// Package text owns the PDF content-stream walker and the TextRun
// type. T2 fills ExtractRuns / ExtractRunsWithOptions; T3 will fill
// the Text field by decoding glyphs through the font's CMap /
// Encoding. T2 leaves Text empty.
package text

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// ExtractOptions controls the behavior of the content-stream walker.
// The zero value is the default: invisible runs (render mode 3)
// suppressed; matches typical text-extraction needs.
type ExtractOptions struct {
	// IncludeInvisible, when true, emits TextRuns whose render mode
	// is 3 (invisible) — useful for accessibility / OCR-overlay
	// pipelines that need to find hidden-text glyphs aligned with a
	// scanned image. Default false suppresses them.
	IncludeInvisible bool
}

// ExtractRuns walks the page's content stream and returns the
// positioned glyph-runs in source order. Equivalent to
// ExtractRunsWithOptions(page, ExtractOptions{}).
//
// The returned runs' Glyphs / CharBounds / CharFlags slices live in a
// throw-away arena that is released to the GC when this function
// returns — fine for one-shot test extraction. Pipeline callers that
// process many pages should use ExtractRunsInto with a pooled arena
// instead so backing slabs are reused across pages.
func ExtractRuns(page *internalpdf.PageObject) ([]TextRun, error) {
	return ExtractRunsWithOptions(page, ExtractOptions{})
}

// ExtractRunsWithOptions is the parameterised entry point. Returns
// (nil, nil) for pages with no content stream. Errors are surfaced
// from the pdfcpu wrapper layer; the walker itself recovers from
// malformed operators (logging a warning and skipping).
func ExtractRunsWithOptions(page *internalpdf.PageObject, opts ExtractOptions) ([]TextRun, error) {
	// One-shot path: allocate a private arena that gets GC'd with the
	// returned runs. Production callers should use ExtractRunsInto.
	return ExtractRunsInto(page, opts, &RunArena{})
}

// ExtractRunsInto is the arena-aware entry. The supplied arena backs
// the per-glyph slice fields (Glyphs, CharBounds, CharFlags) on every
// returned run; release it with ReleaseRunArena (or let it GC) only
// AFTER consumers are done reading from those slices. Passing a
// pooled arena (text.AcquireRunArena) eliminates the per-run slice
// allocations that previously dominated the per-page heap profile.
func ExtractRunsInto(page *internalpdf.PageObject, opts ExtractOptions, arena *RunArena) ([]TextRun, error) {
	if page == nil {
		return nil, errors.New("pdf/text: nil page")
	}
	if arena == nil {
		return nil, errors.New("pdf/text: nil RunArena")
	}
	body, err := page.ContentStream()
	if err != nil {
		return nil, fmt.Errorf("pdf/text: extract content stream: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	w := newWalker(page, opts)
	w.arena = arena
	if err := w.run(body); err != nil {
		return nil, err
	}
	return w.runs, nil
}

// operand is one entry on the walker's operand stack: a token's
// value, decoded into a Go-friendly shape. Floats hold tokInt /
// tokFloat values; strs hold tokName / tokString / tokHexString
// payloads; arr holds an assembled tokArrayStart..tokArrayEnd
// content (kind == tokArrayStart in that case). Mixed arrays carry
// elements whose kinds vary.
type operand struct {
	kind tokKind
	f    float64
	s    []byte
	arr  []operand
}

// walker is the per-extraction state: page handle, options, text +
// graphics state, operand stack, accumulated runs.
type walker struct {
	page      *internalpdf.PageObject
	opts      ExtractOptions
	state     *state
	stack     []operand
	runs      []TextRun
	fontCache map[string]*internalpdf.ResolvedFont
	inText    bool

	// type3Logged tracks Type3 fonts that have already produced a
	// log warning on this page (Phase 8 dedupes warnings per page).
	type3Logged map[string]bool

	// widthFallbackLogged dedupes the "half-em fallback" warning at
	// rung 4 of the width-resolution ladder, once per BaseFont. See
	// glyphAdvance in content_stream_show.go.
	widthFallbackLogged map[string]bool

	// formResourcesStack carries the /Resources dict of each Form
	// XObject the walker is currently recursing into. Empty during
	// page-level walking; non-empty inside a Form whose own
	// /Resources shadows the parent's. resolveFont consults the top
	// of the stack to pick which Resources to resolve fontKey against.
	formResourcesStack []internalpdf.FormResources

	// formDepth is the active Form-XObject recursion depth. Capped at
	// maxFormDepth to bound malicious or accidentally-cyclic Form
	// references.
	formDepth int

	// formStack is the set of Form XObject ObjectKeys currently active
	// on the recursion stack (one per recurseForm frame). Used for
	// cycle detection: if recurseForm is asked to enter a Form whose
	// key is already on the stack, the recurse is skipped to break the
	// cycle. Real-world (Adobe InDesign) PDFs occasionally produce
	// self-referential Form chains; depth-only safeguards either miss
	// them silently (depth ≥ cap) or truncate legitimately-deep nests.
	formStack map[string]int

	// arena backs the per-glyph slice fields on every TextRun emitted
	// by appendRun (Glyphs / CharBounds / CharFlags). Set by
	// ExtractRunsInto; non-nil for the duration of the walk. The
	// fields' slice headers point into the arena's slabs; lifetime is
	// the caller's responsibility (see runarena.go).
	arena *RunArena
}

// maxFormDepth bounds Form XObject recursion. Real PDFs nest at most
// 2-3 deep for hand-authored documents (page → figure with caption
// Form), but Adobe InDesign exports routinely nest 10+ deep wrapping
// each layer/spread/group as a Form. Set to 32 to absorb that without
// leaving practical room for runaway cycles.
const maxFormDepth = 32

// newWalker constructs a walker bound to page with opts.
func newWalker(page *internalpdf.PageObject, opts ExtractOptions) *walker {
	st := newState()
	if page != nil {
		st.pageIndex = page.Index()
	}
	return &walker{
		page:                page,
		opts:                opts,
		state:               st,
		fontCache:           map[string]*internalpdf.ResolvedFont{},
		type3Logged:         map[string]bool{},
		widthFallbackLogged: map[string]bool{},
		formStack:           map[string]int{},
	}
}

// run drives the tokenizer through body, accumulating operands and
// dispatching on each operator. Tokenization errors propagate;
// dispatch-time errors are logged and skipped (real-world PDFs are
// often malformed and we want the rest of the page extracted).
func (w *walker) run(body []byte) error {
	tk := newTokenizer(body)
	for {
		tok, err := tk.next()
		if err != nil {
			return fmt.Errorf("pdf/text: tokenize: %w", err)
		}
		if tok.kind == tokEOF {
			return nil
		}
		if tok.kind == tokArrayStart {
			arr, err := w.readArray(tk)
			if err != nil {
				return err
			}
			w.stack = append(w.stack, operand{kind: tokArrayStart, arr: arr})
			continue
		}
		if tok.kind == tokDictStart {
			// BDC consumes the dict via popDict (Phase 7). Push a
			// placeholder operand carrying the raw dict shape; we
			// build it with readDict to keep nesting safe.
			d, err := w.readDict(tk)
			if err != nil {
				return err
			}
			w.stack = append(w.stack, operand{kind: tokDictStart, arr: d})
			continue
		}
		if tok.kind == tokOperator {
			w.dispatch(string(tok.payload))
			w.stack = w.stack[:0]
			continue
		}
		w.stack = append(w.stack, operandFromToken(tok))
	}
}

// readArray assembles the contents of a tokArrayStart..tokArrayEnd
// block into a flat operand slice. Numbers and strings are pushed
// directly; nested arrays/dicts recurse.
func (w *walker) readArray(tk *tokenizer) ([]operand, error) {
	var out []operand
	for {
		tok, err := tk.next()
		if err != nil {
			return nil, err
		}
		switch tok.kind {
		case tokEOF:
			return nil, errors.New("pdf/text: unterminated array")
		case tokArrayEnd:
			return out, nil
		case tokArrayStart:
			nested, err := w.readArray(tk)
			if err != nil {
				return nil, err
			}
			out = append(out, operand{kind: tokArrayStart, arr: nested})
		case tokDictStart:
			nested, err := w.readDict(tk)
			if err != nil {
				return nil, err
			}
			out = append(out, operand{kind: tokDictStart, arr: nested})
		default:
			out = append(out, operandFromToken(tok))
		}
	}
}

// readDict assembles a tokDictStart..tokDictEnd block. The result
// is flat (k, v, k, v, ...) ordered exactly as in source. T2 only
// cares about the /MCID entry inside BDC's property dict, so the
// flat form is sufficient.
func (w *walker) readDict(tk *tokenizer) ([]operand, error) {
	var out []operand
	for {
		tok, err := tk.next()
		if err != nil {
			return nil, err
		}
		switch tok.kind {
		case tokEOF:
			return nil, errors.New("pdf/text: unterminated dict")
		case tokDictEnd:
			return out, nil
		case tokArrayStart:
			nested, err := w.readArray(tk)
			if err != nil {
				return nil, err
			}
			out = append(out, operand{kind: tokArrayStart, arr: nested})
		case tokDictStart:
			nested, err := w.readDict(tk)
			if err != nil {
				return nil, err
			}
			out = append(out, operand{kind: tokDictStart, arr: nested})
		default:
			out = append(out, operandFromToken(tok))
		}
	}
}

// operandFromToken converts a lexed token into an operand-stack
// entry. Numbers parse to float64 (PDF doesn't strongly distinguish
// int vs float at the operator level); strings/names retain their
// byte payload.
func operandFromToken(tok token) operand {
	o := operand{kind: tok.kind, s: tok.payload}
	if tok.kind == tokInt || tok.kind == tokFloat {
		f, _ := strconv.ParseFloat(string(tok.payload), 64)
		o.f = f
	}
	return o
}

// resolveFont caches ResolvedFont lookups by key. On cache miss it
// calls page.ResolvedFont(key) and stores the result (including nil,
// to avoid re-querying for unknown keys). T3 promoted from
// FontResource → ResolvedFont so the walker can read /Widths,
// /FirstChar, and /MissingWidth at advance time.
func (w *walker) resolveFont(key string) (*internalpdf.ResolvedFont, error) {
	// Inside a Form XObject with its own /Resources, fontKey may shadow
	// the page-level mapping — resolve against the Form's Resources and
	// skip the page-level fontCache to avoid cross-context contamination.
	if n := len(w.formResourcesStack); n > 0 {
		if w.page == nil {
			return nil, nil
		}
		return w.page.ResolvedFontInResources(key, w.formResourcesStack[n-1])
	}
	if cached, ok := w.fontCache[key]; ok {
		return cached, nil
	}
	if w.page == nil {
		w.fontCache[key] = nil
		return nil, nil
	}
	res, err := w.page.ResolvedFont(key)
	if err != nil {
		return nil, err
	}
	w.fontCache[key] = res
	return res, nil
}

// underflow logs a warning that operator op needed `need` operands
// but the stack only has `have`. Mirrors Phase 4's applyBigQ contract.
func (w *walker) underflow(op string, need, have int) {
	slog.Warn("pdf/text: operand-stack underflow",
		"operator", op, "needed", need, "have", have, "page", w.state.pageIndex)
}
