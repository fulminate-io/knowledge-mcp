package pdfcpu

import (
	"errors"
	"fmt"
	"strings"

	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// FontResource is the wrapper-package's surface view of a page-resource
// font dict. T2's content-stream walker emits TextRun values whose font
// fields read directly from this struct: TextRun.FontKey = Key (the
// Tf-operand resource name), TextRun.FontName = BaseFont (PostScript
// name from the font dict), and the Mono/Bold/Italic booleans flow
// from the font descriptor (when present) plus a name-pattern fallback
// for the Standard 14 fonts which legally omit /FontDescriptor.
//
// T3 (font subsystem) reaches deeper into the font dict for CMap /
// Encoding / CIDToGIDMap; T2 needs only these scalar attributes.
type FontResource struct {
	// Key is the resource name from the page's /Resources/Font subdict
	// (e.g. "F1"). This is what content-stream Tf operands name.
	Key string

	// BaseFont is the /BaseFont entry on the font dict (e.g.
	// "Helvetica", "AAAAAA+TimesNewRoman-Bold"). PDF 32000-1:2008 §9.6.2.1
	// names this field as the PostScript name of the font; it is the
	// document-stable identity downstream consumers use to dedupe.
	BaseFont string

	// Subtype is the /Subtype name from the font dict: one of
	// "Type1", "TrueType", "Type0", "Type3", "MMType1", "CIDFontType0",
	// "CIDFontType2".
	Subtype string

	// Mono is true when the font is monospaced. Sourced from the
	// FontDescriptor /Flags bit 1 (FixedPitch) when present;
	// otherwise inferred from the BaseFont containing "Mono" /
	// "Courier".
	Mono bool

	// Bold is true when the font is bold. Sourced from the
	// FontDescriptor /Flags bit 19 (ForceBold) OR a "Bold" / "Heavy"
	// substring on the BaseFont (covers the Standard 14 / subset
	// fonts that omit the descriptor).
	Bold bool

	// Italic is true when the font is italic. Sourced from the
	// FontDescriptor /Flags bit 7 (Italic) OR an "Italic" /
	// "Oblique" substring on the BaseFont.
	Italic bool

	// ObjectKey is the indirect-reference key ("<obj> <gen> R") for
	// the font dict's underlying PDF object, when the dict was reached
	// via an IndirectRef (the common case). Uniquely identifies the
	// font instance across the document — distinct from BaseFont,
	// which can collide when a publisher reuses a subset prefix
	// (e.g. Adobe InDesign exports). Empty when the font dict is
	// inline. Used by collector/pdf/font's resolver to cache decoders
	// without conflating same-BaseFont, different-encoding fonts.
	ObjectKey string
}

// Bit masks for FontDescriptor /Flags. PDF 32000-1:2008 Table 123 numbers
// flag bits 1-32 (1-indexed); the masks below are the corresponding
// 0-indexed shifts: bit 1 -> mask 0x1, bit 7 -> mask 0x40, bit 19 ->
// mask 0x40000. Cross-checked against pdfcpu's own writer
// (font/fontDict.go:272 ttfFontDescriptorFlags) which uses the same
// 1-indexed scheme: FixedPitch -> 0x1, Italic -> 0x40.
const (
	fontFlagFixedPitch = 1 << 0  // bit 1
	fontFlagItalic     = 1 << 6  // bit 7
	fontFlagForceBold  = 1 << 18 // bit 19
)

// FontResource resolves the font keyed `name` in the page's
// /Resources/Font subdict. Returns (nil, nil) — not an error — when:
//   - the page has no /Resources entry,
//   - the Resources dict has no /Font subdict,
//   - the Font subdict has no entry for `name`.
//
// Callers (T2's walker) treat (nil, nil) as "unknown font; emit TextRun
// with empty font fields and continue."
func (p *PageObject) FontResource(name string) (*FontResource, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	if !p.hasAttrs {
		return nil, nil
	}
	return p.FontResourceInResources(name, p.resources)
}

// FontResourceInResources is FontResource against an explicitly-supplied
// /Resources dict instead of the page's. Used by the walker when
// recursing into a Form XObject whose own /Resources shadows the
// parent's. Returns (nil, nil) when resources is nil OR has no
// /Font subdict OR the requested name is missing.
func (p *PageObject) FontResourceInResources(name string, resources FormResources) (*FontResource, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	xRefTable := p.ctx.inner.XRefTable
	if xRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil xref table")
	}
	fontDict, objectKey, err := lookupFontDictInResourcesWithKey(xRefTable, resources, name)
	if err != nil || fontDict == nil {
		return nil, err
	}
	res := &FontResource{Key: name, ObjectKey: objectKey}
	if n := nameEntry(fontDict, "BaseFont"); n != "" {
		res.BaseFont = n
	}
	if n := nameEntry(fontDict, "Subtype"); n != "" {
		res.Subtype = n
	}

	// FontDescriptor is OPTIONAL on the Standard 14 fonts (PDF
	// 32000-1:2008 §9.6.2.2); for those we fall back to a BaseFont
	// substring heuristic.
	flags := fontDescriptorFlags(xRefTable, fontDict)
	res.Mono = flags&fontFlagFixedPitch != 0 || baseFontIsMono(res.BaseFont)
	res.Italic = flags&fontFlagItalic != 0 || baseFontIsItalic(res.BaseFont)
	res.Bold = flags&fontFlagForceBold != 0 || baseFontIsBold(res.BaseFont)
	return res, nil
}

// lookupFontDictInResources walks resources/Font/<name> to return the
// font dict. Returns (nil, nil) when any layer is missing. Wraps
// lookupFontDictInResourcesWithKey for callers (e.g. ResolvedFont
// helpers) that don't need the IndirectRef identity.
func lookupFontDictInResources(xRefTable *pdfmodel.XRefTable, resources pdftypes.Dict, name string) (pdftypes.Dict, error) {
	d, _, err := lookupFontDictInResourcesWithKey(xRefTable, resources, name)
	return d, err
}

// lookupFontDictInResourcesWithKey is the underlying lookup; the
// returned objectKey is the IndirectRef PDFString ("<obj> <gen> R")
// for the font dict's underlying object, or "" when the entry was an
// inline (non-indirect) dict.
func lookupFontDictInResourcesWithKey(xRefTable *pdfmodel.XRefTable, resources pdftypes.Dict, name string) (pdftypes.Dict, string, error) {
	if resources == nil {
		return nil, "", nil
	}
	fontsObj, ok := resources.Find("Font")
	if !ok {
		return nil, "", nil
	}
	fontsDict, err := xRefTable.DereferenceDict(fontsObj)
	if err != nil {
		return nil, "", fmt.Errorf("pdfcpu wrapper: dereference Font subdict: %w", err)
	}
	if fontsDict == nil {
		return nil, "", nil
	}
	fontObj, ok := fontsDict.Find(name)
	if !ok {
		return nil, "", nil
	}
	objectKey := indirectRefKey(fontObj)
	fontDict, err := xRefTable.DereferenceDict(fontObj)
	if err != nil {
		return nil, "", fmt.Errorf("pdfcpu wrapper: dereference font %q: %w", name, err)
	}
	return fontDict, objectKey, nil
}

// fontDescriptorFlags returns the /Flags integer from the font's
// FontDescriptor, or 0 when the descriptor is absent. The Standard 14
// fonts (Helvetica, Times-Roman, Courier, Symbol, ZapfDingbats and
// their bold/italic variants) legally omit the descriptor; readers
// must tolerate its absence.
func fontDescriptorFlags(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict) int {
	fdObj, ok := fontDict.Find("FontDescriptor")
	if !ok {
		return 0
	}
	fdDict, err := xRefTable.DereferenceDict(fdObj)
	if err != nil || fdDict == nil {
		return 0
	}
	o, ok := fdDict.Find("Flags")
	if !ok {
		return 0
	}
	o, err = xRefTable.Dereference(o)
	if err != nil {
		return 0
	}
	if i, ok := o.(pdftypes.Integer); ok {
		return i.Value()
	}
	return 0
}

// nameEntry pulls `key` from `dict` as a PDF Name (string). Returns ""
// when the key is missing or the value is the wrong type.
func nameEntry(dict pdftypes.Dict, key string) string {
	o, ok := dict.Find(key)
	if !ok {
		return ""
	}
	if n, ok := o.(pdftypes.Name); ok {
		return n.Value()
	}
	return ""
}

// baseFontIsBold infers boldness from the BaseFont name. The Standard
// 14 fonts encode style in the name (e.g. "Helvetica-Bold"); subset
// fonts use a hex-prefix + dash variant ("AAAAAA+Helvetica-Bold").
func baseFontIsBold(baseFont string) bool {
	low := strings.ToLower(stripSubsetPrefix(baseFont))
	return strings.Contains(low, "bold") || strings.Contains(low, "heavy") || strings.Contains(low, "black")
}

// baseFontIsItalic infers italics from the BaseFont name.
func baseFontIsItalic(baseFont string) bool {
	low := strings.ToLower(stripSubsetPrefix(baseFont))
	return strings.Contains(low, "italic") || strings.Contains(low, "oblique")
}

// baseFontIsMono infers monospace from the BaseFont name. "Courier"
// covers the Standard 14 monospace family; "Mono" catches subset
// monospace fonts that lack a FontDescriptor.
func baseFontIsMono(baseFont string) bool {
	low := strings.ToLower(stripSubsetPrefix(baseFont))
	return strings.Contains(low, "courier") || strings.Contains(low, "mono")
}

// stripSubsetPrefix removes the "AAAAAA+" subset-tag prefix that
// embedded subset fonts carry. PDF 32000-1:2008 §9.6.4 specifies
// 6 uppercase letters followed by "+".
func stripSubsetPrefix(baseFont string) string {
	if len(baseFont) >= 7 && baseFont[6] == '+' {
		return baseFont[7:]
	}
	return baseFont
}
