package pdfcpu

import (
	"fmt"

	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// readToUnicode populates rf.ToUnicodeBytes from /ToUnicode (when the
// entry is an indirect ref to a Stream object). Sets nil when absent.
// The stream is decoded via StreamDict.Decode() so callers receive the
// CMap source text directly, not the FlateDecode'd input.
func readToUnicode(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict, rf *ResolvedFont) error {
	o, ok := fontDict.Find("ToUnicode")
	if !ok {
		return nil
	}
	sd, _, err := xRefTable.DereferenceStreamDict(o)
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference /ToUnicode stream: %w", err)
	}
	if sd == nil {
		return nil
	}
	if err := sd.Decode(); err != nil {
		return fmt.Errorf("pdfcpu wrapper: decode /ToUnicode stream: %w", err)
	}
	rf.ToUnicodeBytes = append([]byte{}, sd.Content...)
	return nil
}

// readEncoding populates rf.EncodingName, EncodingDictBase, and
// Differences from /Encoding. The entry can be either:
//   - a Name (the predefined encoding case): EncodingName = the name.
//   - a Dict (the override case): EncodingDictBase = /BaseEncoding,
//     Differences = parsed /Differences array.
//
// Errors during dereferencing are logged into the empty-encoding state;
// the resolver will fall back to its 7-rung ladder.
func readEncoding(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict, rf *ResolvedFont) {
	o, ok := fontDict.Find("Encoding")
	if !ok {
		return
	}
	resolved, err := xRefTable.Dereference(o)
	if err != nil {
		return
	}
	switch v := resolved.(type) {
	case pdftypes.Name:
		rf.EncodingName = v.Value()
	case pdftypes.Dict:
		readEncodingDict(xRefTable, v, rf)
	}
}

// readEncodingDict parses an /Encoding dict's /BaseEncoding name and
// /Differences array into rf.EncodingDictBase + rf.Differences.
func readEncodingDict(xRefTable *pdfmodel.XRefTable, dict pdftypes.Dict, rf *ResolvedFont) {
	if n := nameEntry(dict, "BaseEncoding"); n != "" {
		rf.EncodingDictBase = n
	}
	diffsObj, ok := dict.Find("Differences")
	if !ok {
		return
	}
	arr, err := xRefTable.DereferenceArray(diffsObj)
	if err != nil {
		return
	}
	rf.Differences = parseDifferencesArray(arr)
}

// parseDifferencesArray walks the mixed Integer/Name array form
// `[ 32 /space /exclam 65 /A /B ]` and groups names under their
// preceding Integer code. Each group becomes one DifferenceEntry.
func parseDifferencesArray(arr pdftypes.Array) []DifferenceEntry {
	var out []DifferenceEntry
	var cur *DifferenceEntry
	for _, o := range arr {
		switch v := o.(type) {
		case pdftypes.Integer:
			out = append(out, DifferenceEntry{Code: v.Value()})
			cur = &out[len(out)-1]
		case pdftypes.Name:
			if cur == nil {
				// Malformed: name without preceding integer. Skip.
				continue
			}
			cur.Names = append(cur.Names, v.Value())
		}
	}
	return out
}

// readSimpleWidths populates rf.FirstChar and rf.Widths from /FirstChar
// and /Widths. Both are absent on Standard 14 fonts; the resolver falls
// back to the standard14 .dat lookup in that case.
func readSimpleWidths(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict, rf *ResolvedFont) error {
	if fc, ok := fontDict.Find("FirstChar"); ok {
		i, err := xRefTable.DereferenceInteger(fc)
		if err == nil && i != nil {
			rf.FirstChar = i.Value()
		}
	}
	w, ok := fontDict.Find("Widths")
	if !ok {
		return nil
	}
	arr, err := xRefTable.DereferenceArray(w)
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference /Widths: %w", err)
	}
	rf.Widths = make([]int, 0, len(arr))
	for _, o := range arr {
		switch v := o.(type) {
		case pdftypes.Integer:
			rf.Widths = append(rf.Widths, v.Value())
		case pdftypes.Float:
			rf.Widths = append(rf.Widths, int(v.Value()))
		default:
			rf.Widths = append(rf.Widths, 0)
		}
	}
	return nil
}

// fontDescriptorMissingWidth reads /FontDescriptor/MissingWidth as an
// Integer. Returns 0 when the descriptor is absent (Standard 14 fonts)
// or /MissingWidth is missing. Mirror of fontDescriptorFlags() at
// font.go:140.
func fontDescriptorMissingWidth(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict) int {
	fdObj, ok := fontDict.Find("FontDescriptor")
	if !ok {
		return 0
	}
	fdDict, err := xRefTable.DereferenceDict(fdObj)
	if err != nil || fdDict == nil {
		return 0
	}
	mwObj, ok := fdDict.Find("MissingWidth")
	if !ok {
		return 0
	}
	i, err := xRefTable.DereferenceInteger(mwObj)
	if err != nil || i == nil {
		return 0
	}
	return i.Value()
}

// readDescendant populates rf.DescendantFontDict, rf.DescendantSubtype,
// and rf.CIDToGIDIdentity / rf.CIDToGIDMap from /DescendantFonts[0].
// Type 0 fonts only.
func readDescendant(xRefTable *pdfmodel.XRefTable, fontDict pdftypes.Dict, rf *ResolvedFont) error {
	dfObj, ok := fontDict.Find("DescendantFonts")
	if !ok {
		return nil
	}
	arr, err := xRefTable.DereferenceArray(dfObj)
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference /DescendantFonts: %w", err)
	}
	if len(arr) == 0 {
		return nil
	}
	cidDict, err := xRefTable.DereferenceDict(arr[0])
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference DescendantFonts[0]: %w", err)
	}
	if cidDict == nil {
		return nil
	}
	rf.DescendantFontDict = cidDict
	rf.DescendantSubtype = nameEntry(cidDict, "Subtype")
	return readCIDToGIDMap(xRefTable, cidDict, rf)
}

// readCIDToGIDMap populates rf.CIDToGIDIdentity or rf.CIDToGIDMap
// depending on whether /CIDToGIDMap is the Name /Identity or a stream.
func readCIDToGIDMap(xRefTable *pdfmodel.XRefTable, cidDict pdftypes.Dict, rf *ResolvedFont) error {
	o, ok := cidDict.Find("CIDToGIDMap")
	if !ok {
		// Absent — interpret as Identity per PDF 32000-1:2008 §9.7.4.2.
		rf.CIDToGIDIdentity = true
		return nil
	}
	resolved, err := xRefTable.Dereference(o)
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference /CIDToGIDMap: %w", err)
	}
	if n, ok := resolved.(pdftypes.Name); ok && n.Value() == "Identity" {
		rf.CIDToGIDIdentity = true
		return nil
	}
	sd, _, err := xRefTable.DereferenceStreamDict(o)
	if err != nil {
		return fmt.Errorf("pdfcpu wrapper: dereference /CIDToGIDMap stream: %w", err)
	}
	if sd == nil {
		return nil
	}
	if err := sd.Decode(); err != nil {
		return fmt.Errorf("pdfcpu wrapper: decode /CIDToGIDMap stream: %w", err)
	}
	bb := sd.Content
	rf.CIDToGIDMap = make([]uint16, 0, len(bb)/2)
	for i := 0; i+1 < len(bb); i += 2 {
		rf.CIDToGIDMap = append(rf.CIDToGIDMap, uint16(bb[i])<<8|uint16(bb[i+1]))
	}
	return nil
}
