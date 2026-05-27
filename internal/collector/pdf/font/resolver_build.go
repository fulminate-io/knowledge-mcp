package font

import (
	"log/slog"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// buildDecoder assembles a fontDecoder for the given ResolvedFont per
// the locked priority ladder. See file-level godoc in fontDecoder.decode
// for the rung-by-rung breakdown.
//
// Ladder rungs assembled:
//   - Rung 1: ToUnicode CMap   — when /ToUnicode parses
//   - Rung 2: CIDFont decoder  — when Subtype == Type0 (parses descendant cmap)
//   - Rung 3: Encoding+Diff+AGL — assembled from EncodingName +
//     Differences with the implicit-StandardEncoding fallback (per
//     T3-7) for Standard 14 fonts that omit both /ToUnicode and /Encoding.
func buildDecoder(rf *internalpdf.ResolvedFont) (*fontDecoder, error) {
	d := &fontDecoder{}

	// Rung 1: ToUnicode CMap.
	if len(rf.ToUnicodeBytes) > 0 {
		c, err := parseCMap(rf.ToUnicodeBytes)
		if err != nil {
			slog.Warn("pdf/font: ToUnicode parse failed; falling through",
				"baseFont", rf.BaseFont, "err", err.Error())
		} else {
			d.cmap = c
		}
	}

	// Rung 2: CIDFont decoder for Type 0 fonts.
	if rf.Subtype == "Type0" {
		cf, err := newCIDFontDecoder(rf)
		if err == nil {
			d.cidfont = cf
		} else {
			slog.Warn("pdf/font: CIDFont decoder build failed",
				"baseFont", rf.BaseFont, "err", err.Error())
		}
	}

	// Rung 3 prep: simple-font encoding table.
	//
	// Per T3-7: Standard-14-implicit-StandardEncoding fallback per PDF
	// 32000-1:2008 §9.6.2.2. When a font has NO /ToUnicode AND NO
	// /Encoding entry AND its BaseFont matches a Standard 14 name,
	// default to StandardEncoding. This is the spec-mandated default,
	// not a heuristic.
	encName := pickEncodingName(rf)

	if encName != "" || len(rf.Differences) > 0 {
		var base [256]string
		if encName != "" {
			if tab, ok := encodingByName(encName); ok {
				base = *tab
			} else {
				base = fillNotdef()
			}
		} else {
			base = fillNotdef()
		}
		if len(rf.Differences) > 0 {
			base = applyDifferences(base, rf.Differences)
		}
		d.encoding = base
		d.hasEnc = true
	}
	return d, nil
}

// pickEncodingName centralizes the "which encoding table" decision:
//   - If /Encoding is a predefined Name, use it directly.
//   - If /Encoding is a dict with a /BaseEncoding name, use that.
//   - If neither AND no /ToUnicode AND BaseFont is Standard 14, default
//     to StandardEncoding (§9.6.2.2).
//
// Returns "" when no encoding can be resolved — caller checks
// rf.Differences to decide whether to still build an encoding table
// (the diff overrides on top of an all-.notdef base).
func pickEncodingName(rf *internalpdf.ResolvedFont) string {
	if rf.EncodingName != "" {
		return rf.EncodingName
	}
	if rf.EncodingDictBase != "" {
		return rf.EncodingDictBase
	}
	if len(rf.ToUnicodeBytes) == 0 && isStandard14(rf.BaseFont) {
		return "StandardEncoding"
	}
	return ""
}
