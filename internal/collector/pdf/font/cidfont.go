package font

import (
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// cidfontDecoder handles Type 0 (CID-keyed composite) fonts. The
// content-stream walker emits 2-byte CIDs (per T2's bytesToGlyphs at
// content_stream_show.go:70); this decoder maps each CID to runes.
//
// Decode priority:
//  1. /ToUnicode CMap (preferred — Adobe's recommended path for
//     accurate text extraction; PDF 32000-1:2008 §9.10.2).
//  2. /CIDToGIDMap + AGL (stretch — v1 of T3 does not traverse the
//     descendant CIDFont's font program. /CIDToGIDMap maps CID →
//     glyph index, but glyph index → glyph name requires walking the
//     embedded TrueType cmap subtable, which is out of scope for a
//     pure-Go layout-aware text extractor without a font-program
//     parser. Most real-corpus CIDFonts ship a /ToUnicode CMap and
//     hit priority 1.)
//
// When neither path succeeds, decodeCID returns (nil, false) and the
// resolver falls through to its replacement-char ladder rung.
type cidfontDecoder struct {
	cmap        *cmap
	identityMap bool
	gidMap      []uint16
}

// newCIDFontDecoder builds a cidfontDecoder from a ResolvedFont.
// Returns the parser error from parseCMap when /ToUnicode is present
// but malformed; otherwise returns a decoder whose cmap may be nil
// (callers expect that and fall through to the AGL path).
func newCIDFontDecoder(rf *internalpdf.ResolvedFont) (*cidfontDecoder, error) {
	d := &cidfontDecoder{
		identityMap: rf.CIDToGIDIdentity,
		gidMap:      rf.CIDToGIDMap,
	}
	if len(rf.ToUnicodeBytes) > 0 {
		c, err := parseCMap(rf.ToUnicodeBytes)
		if err != nil {
			return nil, err
		}
		d.cmap = c
	}
	return d, nil
}

// decodeCID maps a 2-byte (or up to 4-byte) CID to runes via the
// preferred /ToUnicode CMap path. v1 stops at priority 1; priority 2
// (CIDToGIDMap + AGL) is documented as a known stretch on the type's
// godoc.
func (d *cidfontDecoder) decodeCID(cid uint32) ([]rune, bool) {
	if d.cmap != nil {
		if rs, ok := d.cmap.decode(cid); ok {
			return rs, true
		}
	}
	return nil, false
}

// decodeCIDInto is decodeCID's alloc-free twin; writes runes directly
// to b. Returns true when a mapping was found.
func (d *cidfontDecoder) decodeCIDInto(cid uint32, b stringWriter) bool {
	if d.cmap == nil {
		return false
	}
	return d.cmap.decodeInto(cid, b)
}

// hasToUnicode reports whether the decoder has a parsed /ToUnicode
// CMap. Used by the resolver to decide whether to surface a
// "no decoder available" warning when both /ToUnicode and /CIDToGIDMap
// are absent.
func (d *cidfontDecoder) hasToUnicode() bool {
	return d != nil && d.cmap != nil
}
