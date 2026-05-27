package font

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// Run is the minimal surface a TextRun-like value must expose for
// decoding. Defined here as an interface so the font package does NOT
// import collector/pdf/text — text imports font (for standard14Width
// in the width ladder). Breaking the back-edge keeps the dependency
// graph acyclic.
//
// The text.TextRun value type satisfies this contract via a small
// adapter (textRunAdapter in collector/pdf/page.go). The public
// pdf.Page.TextRuns caller wraps a slice of TextRun in such adapters
// before passing to font.Decode.
//
// SetCharFlags receives one bitmask byte per glyph (parallel to the
// run's Glyphs slice). The adapter assigns the slice onto the
// TextRun's CharFlags field. Resolver paths that cannot identify a
// glyph (rung 4 fallback) set the CharFlagBadMap bit; clean rung-1/2/3
// hits leave the byte zero.
//
// FontResourcesHint returns the active Form XObject /Resources dict
// for the run, or nil for page-level runs. When non-nil, the resolver
// routes the fontKey lookup through page.ResolvedFontInResources(key,
// hint) and bypasses the per-resolver decoder cache (a single fontKey
// can resolve to different fonts in different Form contexts; the
// page-level cache key is not Resources-aware). When nil, the resolver
// falls back to page.ResolvedFont(key) — the existing T3 path.
type Run interface {
	GlyphsCopy() []uint16
	FontKeyValue() string
	FontResourcesHint() internalpdf.FormResources
	SetText(s string)
	SetCharFlags(flags []uint8)
}

// Decode populates run.Text on each Run in-place by resolving its
// Glyphs against page's font resources. Returns nil on success; any
// per-run lookup error short-circuits and surfaces.
//
// For batch decoding many pages from the same Document, prefer
// NewDocResolver(ctx) + DecodePage which shares parsed CMap state.
func Decode(runs []Run, page *internalpdf.PageObject) error {
	if len(runs) == 0 {
		return nil
	}
	return NewResolver(page).Decode(runs)
}

// FontResolver caches per-font decoder state (parsed CMap, encoding
// table, CIDFont decoder) keyed on stable font identity (BaseFont +
// sha256(ToUnicodeBytes)). Use NewDocResolver for batch decoding many
// pages from the same Document; use NewResolver for the single-page
// convenience case.
//
// Thread-safe: cache lookups use sync.Map; lookupDecoder serializes
// build per missing key via LoadOrStore semantics.
type FontResolver struct {
	page  *internalpdf.PageObject // pinned for single-page convenience; nil for doc-scope
	ctx   *internalpdf.Context    // doc-scope source for ResolvedFont lookups
	cache sync.Map                // map[string]*fontDecoder, keyed by BaseFont + ":" + sha256-prefix(ToUnicodeBytes)
}

// NewResolver constructs a page-scoped FontResolver. The cache lives
// only as long as this resolver; caches do NOT cross calls. Use this
// for one-off page decodes.
func NewResolver(page *internalpdf.PageObject) *FontResolver {
	var ctx *internalpdf.Context
	if page != nil {
		ctx = page.Context()
	}
	return &FontResolver{page: page, ctx: ctx}
}

// NewDocResolver constructs a Document-scoped FontResolver. Cache is
// keyed on stable font identity (BaseFont + sha256(ToUnicodeBytes))
// so 100 pages from the same Document re-use parsed CMap state on the
// second-and-subsequent encounters of each font. Satisfies the ticket
// DoD bullet "FontResolver caching test: decoding 100 pages from the
// same Document re-uses parsed CMap state".
//
// Cache key rationale: BaseFont alone is insufficient (different fonts
// can share a BaseFont via subsetting); ToUnicode bytes alone is too
// narrow (Standard 14 fonts have no ToUnicode). The composite key
// dedupes on the union of identity. sha256 is collision-resistant; we
// keep an 8-byte prefix in the key for compactness — collision risk
// in a typical document (a handful of fonts) is negligible.
func NewDocResolver(ctx *internalpdf.Context) *FontResolver {
	return &FontResolver{ctx: ctx}
}

// Decode populates the Text field on each Run using the resolver's
// pinned page (set by NewResolver). For doc-scope resolvers built via
// NewDocResolver, callers should use DecodePage to supply the page
// each batch belongs to.
func (r *FontResolver) Decode(runs []Run) error {
	return r.DecodePage(runs, r.page)
}

// DecodePage decodes runs from a specific page using the resolver's
// shared cache. NewDocResolver callers use this; NewResolver callers
// use Decode (which forwards here with the pinned page).
func (r *FontResolver) DecodePage(runs []Run, page *internalpdf.PageObject) error {
	if page == nil {
		return nil
	}
	for _, run := range runs {
		if err := r.decodeRun(run, page); err != nil {
			return err
		}
	}
	return nil
}

// decodeGlyphsWithFlags turns a glyph sequence for a given font key
// on a given page into UTF-8 text plus a parallel per-glyph flag
// slice. Used internally by decodeRun. Bit charFlagBadMap is set on
// entries the decoder could not map (the U+FFFD path). Empty input
// returns ("", nil, nil). When hint is non-nil the resolver looks up
// fontKey in the Form XObject's /Resources rather than the page-level
// Resources; see lookupDecoder for the cache-bypass rationale.
func (r *FontResolver) decodeGlyphsWithFlags(glyphs []uint16, fontKey string, hint internalpdf.FormResources, page *internalpdf.PageObject) (string, []uint8, error) {
	if page == nil {
		return "", nil, nil
	}
	dec, err := r.lookupDecoder(fontKey, hint, page)
	if err != nil {
		return "", nil, err
	}
	s, flags := resolveGlyphsWithFlags(dec, glyphs, fontKey, page.Index())
	return s, flags, nil
}

// charFlagBadMap mirrors text.CharFlagBadMap (= 1 << 1). Duplicated
// here so font/ does not import text/ (text imports font for the
// width ladder; the back-edge would create an import cycle). The two
// declarations carry the same bit value by contract — see
// collector/pdf/text/types.go for the canonical CharFlag* set.
const charFlagBadMap uint8 = 1 << 1

// resolveGlyphsWithFlags walks glyphs against dec and assembles the
// UTF-8 text. The second return is a bitmask slice parallel to glyphs
// (one byte per glyph). For each glyph that hits the rung-4
// replacement-char fallback, the corresponding byte has
// charFlagBadMap set and a slog.Warn names the page+fontKey context;
// clean rung-1/2/3 hits leave the byte zero.
func resolveGlyphsWithFlags(dec *fontDecoder, glyphs []uint16, fontKey string, pageIndex int) (string, []uint8) {
	var b strings.Builder
	b.Grow(len(glyphs))
	flags := make([]uint8, len(glyphs))
	for i, g := range glyphs {
		if dec.decodeInto(uint32(g), &b) {
			continue
		}
		_, _ = b.WriteRune(0xFFFD)
		flags[i] |= charFlagBadMap
		slog.Warn("pdf/font: glyph not resolvable",
			"page", pageIndex, "fontKey", fontKey, "glyph", g)
	}
	return b.String(), flags
}

// decodeRun resolves the glyph sequence on a single Run via the Run
// adapter interface. Sets run.SetText to the UTF-8 result and
// run.SetCharFlags to the parallel per-glyph flag slice (rung-4 hits
// carry charFlagBadMap; clean hits are zero).
func (r *FontResolver) decodeRun(run Run, page *internalpdf.PageObject) error {
	text, flags, err := r.decodeGlyphsWithFlags(run.GlyphsCopy(), run.FontKeyValue(), run.FontResourcesHint(), page)
	if err != nil {
		return err
	}
	run.SetText(text)
	run.SetCharFlags(flags)
	return nil
}

// fontDecoder is the per-font resolved state assembled by buildDecoder.
// Its decode method walks the resolution ladder per PDF 32000-1:2008
// §9.6.6 + §9.10 in the order locked in this package's documentation.
type fontDecoder struct {
	cmap     *cmap
	encoding [256]string
	hasEnc   bool
	cidfont  *cidfontDecoder
}

// decode walks the resolution ladder rungs 1-3. Rung 4 (replacement
// char) lives in decodeRun above.
func (d *fontDecoder) decode(cid uint32) ([]rune, bool) {
	// Rung 1: /ToUnicode CMap (preferred per Adobe Tech Note #5014).
	if d.cmap != nil {
		if rs, ok := d.cmap.decode(cid); ok {
			return rs, true
		}
	}
	// Rung 2: CIDFont decoder for Type0 (descendant ToUnicode path).
	if d.cidfont != nil {
		if rs, ok := d.cidfont.decodeCID(cid); ok {
			return rs, true
		}
	}
	// Rung 3: simple-font /Encoding table → glyph name → AGL.
	if d.hasEnc && cid < 256 {
		name := d.encoding[cid]
		if name != "" && name != notdef {
			if rs, ok := lookupGlyph(name); ok {
				return rs, true
			}
		}
	}
	return nil, false
}

// decodeInto walks the same rungs as decode but writes the resulting
// runes directly to b. Alloc-free hot path: avoids the per-glyph
// []rune allocation that lookupRange's sequential branch otherwise
// pays. resolveGlyphsWithFlags uses this against a strings.Builder
// so a per-page run of N glyphs allocates only the final string,
// not N intermediate slices.
func (d *fontDecoder) decodeInto(cid uint32, b stringWriter) bool {
	if d.cmap != nil {
		if d.cmap.decodeInto(cid, b) {
			return true
		}
	}
	if d.cidfont != nil {
		if d.cidfont.decodeCIDInto(cid, b) {
			return true
		}
	}
	if d.hasEnc && cid < 256 {
		name := d.encoding[cid]
		if name != "" && name != notdef {
			if rs, ok := lookupGlyph(name); ok {
				for _, rn := range rs {
					_, _ = b.WriteRune(rn)
				}
				return true
			}
		}
	}
	return false
}

// lookupDecoder returns the cached fontDecoder for the named font key,
// building it on cache miss. Unknown fontKey resolves to an empty
// decoder which produces all-replacement-char output (rung 4 fires
// for every glyph).
//
// When hint is non-nil the run was emitted inside a Form XObject whose
// own /Resources defines (or shadows) fonts — resolve via
// page.ResolvedFontInResources(fontKey, hint) and skip the per-resolver
// decoder cache. Mirrors walker.resolveFont's cache-bypass at
// collector/pdf/text/content_stream.go:254-258 so two different Form
// Resources can legally define the same fontKey (e.g. T1_0) without
// the page-level cache substituting one decoder for the other. No
// cascade to page-level on miss: a hint that lacks fontKey returns the
// empty decoder, matching walker.resolveFont's no-cascade semantics
// (the Form's Resources shadows the parent dict; a missing key is the
// Form's responsibility, not a fall-through).
func (r *FontResolver) lookupDecoder(fontKey string, hint internalpdf.FormResources, page *internalpdf.PageObject) (*fontDecoder, error) {
	if hint != nil {
		rf, err := page.ResolvedFontInResources(fontKey, hint)
		if err != nil {
			return nil, err
		}
		if rf == nil {
			// Unknown FontKey in the Form's Resources — empty decoder,
			// no cascade to page-level (walker.resolveFont parity).
			return &fontDecoder{}, nil
		}
		// Cache-bypass: build a fresh decoder, do not Store it. The
		// page-level cache key is BaseFont + ToUnicode hash and would
		// collide across distinct Form contexts that share a BaseFont.
		return buildDecoder(rf)
	}

	// Fast path: look up the font's IndirectRef key via the cheap
	// FontResource accessor (no ToUnicode read) and check the
	// objectkey-cache. ObjectKey uniquely identifies the font
	// instance across the document — distinct from BaseFont, which
	// can collide when a publisher reuses subset prefixes (Adobe
	// InDesign exports do this routinely).
	if base, _ := page.FontResource(fontKey); base != nil && base.ObjectKey != "" {
		if cached, ok := r.cache.Load("objkey:" + base.ObjectKey); ok {
			if dec, ok2 := cached.(*fontDecoder); ok2 {
				return dec, nil
			}
		}
	}

	rf, err := page.ResolvedFont(fontKey)
	if err != nil {
		return nil, err
	}
	if rf == nil {
		// Unknown FontKey — return an empty decoder per-call, NOT
		// cached (the absence is page-specific, not font-identity).
		return &fontDecoder{}, nil
	}
	// Slow-path cache key: BaseFont + Subtype + sha256(ToUnicodeBytes)
	// prefix. ToUnicode hash distinguishes fonts that share
	// BaseFont+Subtype but ship different /ToUnicode CMaps. The
	// objectkey fast-path above is the primary cache; this hash key
	// is the fallback when the same logical font dict is reached via
	// a different IndirectRef on a different page (rare but legal).
	sum := sha256.Sum256(rf.ToUnicodeBytes)
	key := rf.BaseFont + ":" + rf.Subtype + ":" + hex.EncodeToString(sum[:8])
	if cached, ok := r.cache.Load(key); ok {
		if dec, ok2 := cached.(*fontDecoder); ok2 {
			if rf.ObjectKey != "" {
				r.cache.LoadOrStore("objkey:"+rf.ObjectKey, dec)
			}
			return dec, nil
		}
	}
	d, err := buildDecoder(rf)
	if err != nil {
		return nil, err
	}
	actual, _ := r.cache.LoadOrStore(key, d)
	dec, _ := actual.(*fontDecoder)
	if dec == nil {
		dec = d
	}
	if rf.ObjectKey != "" {
		r.cache.LoadOrStore("objkey:"+rf.ObjectKey, dec)
	}
	return dec, nil
}
