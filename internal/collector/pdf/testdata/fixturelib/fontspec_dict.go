package fixturelib

import (
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// buildType0FontDict (called when fs.Subtype == "Type0").
func buildFontDict(xRefTable *model.XRefTable, fs FontSpec) (*types.IndirectRef, error) {
	if fs.Subtype == "Type0" {
		return buildType0FontDict(xRefTable, fs)
	}
	d := types.NewDict()
	d.InsertName("Type", "Font")
	subtype := fs.Subtype
	if subtype == "" {
		subtype = "Type1"
	}
	d.InsertName("Subtype", subtype)
	d.InsertName("BaseFont", fs.BaseFont)
	if err := writeEncoding(xRefTable, d, fs); err != nil {
		return nil, err
	}
	writeWidths(d, fs)
	if err := writeToUnicode(xRefTable, d, fs); err != nil {
		return nil, err
	}
	if fs.MissingWidth != 0 {
		if err := writeFontDescriptor(xRefTable, d, fs); err != nil {
			return nil, err
		}
	}
	return xRefTable.IndRefForNewObject(d)
}

// writeEncoding writes /Encoding to the font dict. When Differences is
// non-empty, an Encoding dict with /BaseEncoding + /Differences is
// emitted; otherwise the predefined-name form is used.
func writeEncoding(xRefTable *model.XRefTable, fontDict types.Dict, fs FontSpec) error {
	if len(fs.Differences) == 0 {
		if fs.Encoding != "" {
			fontDict.InsertName("Encoding", fs.Encoding)
		}
		return nil
	}
	encDict := types.NewDict()
	encDict.InsertName("Type", "Encoding")
	if fs.Encoding != "" {
		encDict.InsertName("BaseEncoding", fs.Encoding)
	}
	arr := types.Array{}
	for _, d := range fs.Differences {
		arr = append(arr, types.Integer(d.Code))
		for _, n := range d.Names {
			arr = append(arr, types.Name(n))
		}
	}
	encDict.Insert("Differences", arr)
	encRef, err := xRefTable.IndRefForNewObject(encDict)
	if err != nil {
		return fmt.Errorf("encoding indref: %w", err)
	}
	fontDict.Insert("Encoding", *encRef)
	return nil
}

// writeWidths writes /FirstChar, /LastChar, /Widths to the font dict
// when fs.Widths is non-empty.
func writeWidths(fontDict types.Dict, fs FontSpec) {
	if len(fs.Widths) == 0 {
		return
	}
	fontDict.InsertInt("FirstChar", fs.FirstChar)
	fontDict.InsertInt("LastChar", fs.FirstChar+len(fs.Widths)-1)
	arr := make(types.Array, 0, len(fs.Widths))
	for _, w := range fs.Widths {
		arr = append(arr, types.Integer(w))
	}
	fontDict.Insert("Widths", arr)
}

// writeToUnicode writes a /ToUnicode stream to the font dict when
// fs.ToUnicodeBytes is non-empty. The stream is FlateDecode'd by
// pdfcpu's StreamDict.Encode automatically.
func writeToUnicode(xRefTable *model.XRefTable, fontDict types.Dict, fs FontSpec) error {
	if len(fs.ToUnicodeBytes) == 0 {
		return nil
	}
	sd, err := xRefTable.NewStreamDictForBuf(fs.ToUnicodeBytes)
	if err != nil {
		return fmt.Errorf("new ToUnicode stream: %w", err)
	}
	if err := sd.Encode(); err != nil {
		return fmt.Errorf("encode ToUnicode stream: %w", err)
	}
	ir, err := xRefTable.IndRefForNewObject(*sd)
	if err != nil {
		return fmt.Errorf("ToUnicode indref: %w", err)
	}
	fontDict.Insert("ToUnicode", *ir)
	return nil
}

// writeFontDescriptor emits a minimal FontDescriptor with /MissingWidth.
// Used by fixtures that need to exercise the FontDescriptor read path
// (e.g. the /MissingWidth fallback rung of the width-resolution ladder).
func writeFontDescriptor(xRefTable *model.XRefTable, fontDict types.Dict, fs FontSpec) error {
	fd := types.NewDict()
	fd.InsertName("Type", "FontDescriptor")
	fd.InsertName("FontName", fs.BaseFont)
	fd.InsertInt("MissingWidth", fs.MissingWidth)
	// Pinned defaults so pdfcpu's validator is satisfied.
	fd.InsertInt("Flags", 32) // bit 6 = nonsymbolic
	fd.InsertInt("ItalicAngle", 0)
	fd.InsertInt("Ascent", 750)
	fd.InsertInt("Descent", -250)
	fd.InsertInt("CapHeight", 700)
	fd.InsertInt("StemV", 80)
	fd.Insert("FontBBox", types.Array{types.Integer(-100), types.Integer(-200), types.Integer(1000), types.Integer(900)})
	ir, err := xRefTable.IndRefForNewObject(fd)
	if err != nil {
		return fmt.Errorf("FontDescriptor indref: %w", err)
	}
	fontDict.Insert("FontDescriptor", *ir)
	return nil
}
