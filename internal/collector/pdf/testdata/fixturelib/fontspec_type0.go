package fixturelib

import (
	"encoding/binary"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// buildType0FontDict assembles a Type 0 (composite) font dict and
// returns its indirect ref. The Type 0 dict references a single
// descendant CIDFont via /DescendantFonts; the descendant carries the
// /CIDToGIDMap (Identity name or stream) and CID-keyed metrics.
//
// Layout per PDF 32000-1:2008 §9.7.2:
//
//	<<
//	  /Type /Font
//	  /Subtype /Type0
//	  /BaseFont /<name>
//	  /Encoding /Identity-H
//	  /DescendantFonts [<cidfont indref>]
//	  /ToUnicode <cmap indref>
//	>>
//
//	cidfont:
//	<<
//	  /Type /Font
//	  /Subtype /CIDFontType0 | /CIDFontType2
//	  /BaseFont /<name>
//	  /CIDSystemInfo <<...>>
//	  /CIDToGIDMap /Identity | <stream>
//	  /FontDescriptor <descriptor indref>
//	>>
func buildType0FontDict(xRefTable *model.XRefTable, fs FontSpec) (*types.IndirectRef, error) {
	cidIR, err := buildCIDFontDict(xRefTable, fs)
	if err != nil {
		return nil, err
	}
	d := types.NewDict()
	d.InsertName("Type", "Font")
	d.InsertName("Subtype", "Type0")
	d.InsertName("BaseFont", fs.BaseFont)
	encName := fs.Encoding
	if encName == "" {
		encName = "Identity-H"
	}
	d.InsertName("Encoding", encName)
	d.Insert("DescendantFonts", types.Array{*cidIR})
	if err := writeToUnicode(xRefTable, d, fs); err != nil {
		return nil, err
	}
	return xRefTable.IndRefForNewObject(d)
}

// buildCIDFontDict emits the descendant CIDFont dict for a Type 0 font.
// /Subtype is taken from FontSpec.DescendantSubtype (defaults to
// "CIDFontType2"). /CIDToGIDMap is /Identity when fs.CIDToGIDIdentity
// is set; otherwise a stream of fs.CIDToGIDMap bytes (big-endian uint16
// per CID).
func buildCIDFontDict(xRefTable *model.XRefTable, fs FontSpec) (*types.IndirectRef, error) {
	cd := types.NewDict()
	cd.InsertName("Type", "Font")
	subtype := fs.DescendantSubtype
	if subtype == "" {
		subtype = "CIDFontType2"
	}
	cd.InsertName("Subtype", subtype)
	cd.InsertName("BaseFont", fs.BaseFont)

	// /CIDSystemInfo (required, per §9.7.3 Table 117).
	csi := types.NewDict()
	csi.InsertString("Registry", "Adobe")
	csi.InsertString("Ordering", "Identity")
	csi.InsertInt("Supplement", 0)
	cd.Insert("CIDSystemInfo", csi)

	// /CIDToGIDMap.
	if fs.CIDToGIDIdentity {
		cd.InsertName("CIDToGIDMap", "Identity")
	} else if len(fs.CIDToGIDMap) > 0 {
		buf := make([]byte, 2*len(fs.CIDToGIDMap))
		for i, gid := range fs.CIDToGIDMap {
			binary.BigEndian.PutUint16(buf[2*i:], gid)
		}
		sd, err := xRefTable.NewStreamDictForBuf(buf)
		if err != nil {
			return nil, fmt.Errorf("new CIDToGIDMap stream: %w", err)
		}
		if err := sd.Encode(); err != nil {
			return nil, fmt.Errorf("encode CIDToGIDMap stream: %w", err)
		}
		ir, err := xRefTable.IndRefForNewObject(*sd)
		if err != nil {
			return nil, fmt.Errorf("CIDToGIDMap indref: %w", err)
		}
		cd.Insert("CIDToGIDMap", *ir)
	}

	// /FontDescriptor — minimal, satisfies pdfcpu's validator.
	fd := types.NewDict()
	fd.InsertName("Type", "FontDescriptor")
	fd.InsertName("FontName", fs.BaseFont)
	fd.InsertInt("Flags", 4) // symbolic
	fd.InsertInt("ItalicAngle", 0)
	fd.InsertInt("Ascent", 750)
	fd.InsertInt("Descent", -250)
	fd.InsertInt("CapHeight", 700)
	fd.InsertInt("StemV", 80)
	fd.Insert("FontBBox", types.Array{types.Integer(-100), types.Integer(-200), types.Integer(1000), types.Integer(900)})
	if fs.MissingWidth != 0 {
		fd.InsertInt("MissingWidth", fs.MissingWidth)
	}
	fdIR, err := xRefTable.IndRefForNewObject(fd)
	if err != nil {
		return nil, fmt.Errorf("FontDescriptor indref: %w", err)
	}
	cd.Insert("FontDescriptor", *fdIR)

	return xRefTable.IndRefForNewObject(cd)
}
