package pdfcpu

import (
	"errors"

	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ResolvedFont is the wrapper-package's deep-read view of a page-resource
// font dict. It composes the lightweight FontResource via embedding (T2's
// Tf-cache surface) with the heavier fields T3's font resolver needs:
// /ToUnicode CMap stream bytes, /Encoding name + /Differences override,
// /CIDToGIDMap (Identity vs stream bytes), /DescendantFonts pointer,
// /FirstChar + /Widths array, and FontDescriptor /MissingWidth.
//
// Constructed via (p *PageObject).ResolvedFont(name). Populated lazily
// (one dict-walk per call); the resolver caches across calls.
//
// Field semantics match PDF 32000-1:2008:
//   - §9.10.2 (ToUnicode): ToUnicodeBytes is the DECODED stream bytes
//     (FlateDecode etc. applied) ready for parseCMap. nil when absent.
//   - §9.6.2 / Annex D (Encoding): EncodingName is the predefined name
//     ("WinAnsiEncoding" etc.) when /Encoding is a Name; Differences
//     is populated when /Encoding is a dict with a /Differences array.
//   - §9.7.4 (CIDToGIDMap): Identity is true when the entry is the Name
//     /Identity; CIDToGIDMap is populated for stream form (2 bytes per
//     CID, big-endian).
//   - §9.7.3 (DescendantFonts): a 1-element array per the spec; we
//     dereference the [0] dict and store its /Subtype separately.
//   - §9.6.2.1 (FirstChar / Widths): /Widths[i] is the advance width of
//     code (FirstChar+i). Both zero when absent (Standard 14 fonts).
//   - §9.8.2 (MissingWidth): default width for codes outside the
//     /FirstChar..LastChar range, in 1/1000 em.
type ResolvedFont struct {
	*FontResource

	// ToUnicodeBytes is the DECODED /ToUnicode stream content, ready
	// for parseCMap. nil when the font has no /ToUnicode entry.
	ToUnicodeBytes []byte

	// EncodingName is the /Encoding name ("WinAnsiEncoding",
	// "MacRomanEncoding", "StandardEncoding", "MacExpertEncoding"),
	// or empty when /Encoding is a dict (in which case Differences
	// holds the override, and the resolver must source the base
	// encoding from EncodingDictBase below).
	EncodingName string

	// EncodingDictBase is the /BaseEncoding name when /Encoding is a
	// dict (rather than a top-level Name). Empty when /Encoding is a
	// top-level Name (EncodingName carries it instead) or absent.
	EncodingDictBase string

	// Differences are the (code, glyph-name) overrides from a
	// /Differences array inside an /Encoding dict. Empty when no
	// /Differences entry was found.
	Differences []DifferenceEntry

	// CIDToGIDIdentity is true when the font's /CIDToGIDMap entry is
	// the Name /Identity. Mutually exclusive with CIDToGIDMap.
	CIDToGIDIdentity bool

	// CIDToGIDMap is the decoded /CIDToGIDMap stream bytes interpreted
	// as a uint16 array (big-endian, 2 bytes per CID). nil when the
	// entry is absent or the Identity name.
	CIDToGIDMap []uint16

	// DescendantFontDict is the dereferenced /DescendantFonts[0] dict
	// (Type 0 fonts only). Carries the CIDFontType0/CIDFontType2
	// subtype and CID-keyed metrics. nil for non-Type0 fonts.
	DescendantFontDict pdftypes.Dict

	// DescendantSubtype is the /Subtype Name on the DescendantFontDict
	// ("CIDFontType0" or "CIDFontType2"). Empty for non-Type0 fonts.
	DescendantSubtype string

	// FirstChar is the /FirstChar integer (the code of Widths[0]).
	// Zero when /FirstChar is absent (Standard 14 fonts can omit).
	FirstChar int

	// Widths is the /Widths array (advance width per code in 1/1000 em).
	// Indexed by (code - FirstChar). nil when /Widths is absent.
	Widths []int

	// MissingWidth is the FontDescriptor /MissingWidth integer (the
	// default advance for codes outside [FirstChar..FirstChar+len(Widths)-1]).
	// Zero when absent — the standard14 fallback fires next.
	MissingWidth int
}

// DifferenceEntry represents one run of glyph names in a /Differences
// array. Code is the starting code; Names lists the glyph names mapped
// sequentially (Names[0] → Code, Names[1] → Code+1, ...).
type DifferenceEntry struct {
	Code  int
	Names []string
}

// ResolvedFont returns the deep-read view of the page-resource font keyed
// `name`. Returns (nil, nil) — not an error — when the font dict isn't
// present, mirroring FontResource's contract.
//
// Uses the /Resources dict cached on PageObject at construction time —
// avoiding the per-call PageDict walk that previously dominated CPU on
// multi-page documents (491 pages × N fonts × O(page-tree-depth)).
func (p *PageObject) ResolvedFont(name string) (*ResolvedFont, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	if !p.hasAttrs {
		return nil, nil
	}
	return p.ResolvedFontInResources(name, p.resources)
}

// ResolvedFontInResources is ResolvedFont against an explicitly-
// supplied /Resources dict instead of the page's. Used by the walker
// when recursing into a Form XObject whose own /Resources shadows the
// parent's. Returns (nil, nil) when resources is nil OR the font dict
// is missing.
func (p *PageObject) ResolvedFontInResources(name string, resources FormResources) (*ResolvedFont, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	xRefTable := p.ctx.inner.XRefTable
	if xRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil xref table")
	}
	base, err := p.FontResourceInResources(name, resources)
	if err != nil || base == nil {
		return nil, err
	}
	fontDict, err := lookupFontDictInResources(xRefTable, resources, name)
	if err != nil {
		return nil, err
	}
	if fontDict == nil {
		return nil, nil
	}
	rf := &ResolvedFont{FontResource: base}
	if err := readToUnicode(xRefTable, fontDict, rf); err != nil {
		return nil, err
	}
	readEncoding(xRefTable, fontDict, rf)
	if err := readSimpleWidths(xRefTable, fontDict, rf); err != nil {
		return nil, err
	}
	rf.MissingWidth = fontDescriptorMissingWidth(xRefTable, fontDict)

	// Type 0 (composite) fonts carry CID-keyed widths and CIDToGIDMap
	// behind /DescendantFonts[0]. Walk that subtree only when present.
	if rf.Subtype == "Type0" {
		if err := readDescendant(xRefTable, fontDict, rf); err != nil {
			return nil, err
		}
	}
	return rf, nil
}
