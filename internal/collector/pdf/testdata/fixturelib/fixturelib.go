// Package fixturelib provides shared helpers for the PDF fixture
// generators under collector/pdf/testdata. It is a real Go package
// (no //go:build ignore) but lives under testdata/ so the Go tool
// excludes it from production builds via the testdata convention.
//
// gen.go (the orchestrator at collector/pdf/testdata/gen.go) imports
// fixturelib and calls Write*PDF helpers; tests under
// collector/pdf/text/ and collector/pdf/font/ do NOT import fixturelib
// — they read the pre-emitted .pdf files that gen.go writes.
package fixturelib

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// PageSpec is one page worth of content stream + resource setup.
// Fonts is a slice of FontSpec — each describing a /Resources/Font
// entry on the page. Body is the raw content-stream operator
// sequence.
//
// T2 callers used a `map[string]string` shape (resource key →
// BaseFont). T3 promoted to []FontSpec so fixtures can drive the full
// font-dict surface (/Encoding, /Differences, /ToUnicode, /Widths,
// /CIDToGIDMap). Migration helpers live in fontspec.go:
// SimpleFontSpec, SimpleFontSpecMap.
type PageSpec struct {
	Fonts []FontSpec
	Body  string

	// Rotation is the page /Rotate value (one of 0, 90, 180, 270).
	// Zero (default) emits no /Rotate entry on the page dict.
	// Added by T4 to drive the rotated-page integration fixture.
	Rotation int
}

// WritePDF emits a single-page PDF for the given spec. dst is the
// absolute output path; parent directories are created if missing.
//
// Single-page fixtures use this entry point. Multi-page fixtures use
// WriteMultiPagePDF in fontspec.go (which lets pages share font
// indirect-refs for the caching test fixture).
//
// Implementation: this is now a one-page wrapper over WriteMultiPagePDF
// — the spec's Fonts slice promotes to sharedFonts.
func WritePDF(dst string, specs []PageSpec) error {
	if len(specs) == 0 {
		return fmt.Errorf("WritePDF: no specs (need at least 1 page)")
	}
	if len(specs) > 1 {
		return fmt.Errorf("WritePDF: multi-page input (%d pages); use WriteMultiPagePDF for shared fonts", len(specs))
	}
	return WriteMultiPagePDF(dst, specs, specs[0].Fonts)
}

// OnePagePDFBody is the baseline content stream used by onepage.pdf:
// "Hello, T1" rendered in 12pt Helvetica at (100, 700). Sharable
// between gen.go and any future test-only consumers.
func OnePagePDFBody(text string) string {
	return fmt.Sprintf("BT /F1 12 Tf 100 700 Td (%s) Tj ET\n", text)
}

// ParagraphBody emits 3 lines of body text via T*-driven leading.
// Each line carries its own trailing newline operator so the text
// matrix advances predictably.
func ParagraphBody(line1, line2, line3 string) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf 14 TL 100 700 Td")
	fmt.Fprintf(&b, "(%s) Tj T*\n", line1)
	fmt.Fprintf(&b, "(%s) Tj T*\n", line2)
	fmt.Fprintf(&b, "(%s) Tj\n", line3)
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// TJKerningBody emits a TJ array with two -100 kerning adjustments
// between the three string fragments.
func TJKerningBody(left, mid, right string) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf 100 700 Td")
	fmt.Fprintf(&b, "[(%s) -100 (%s) -50 (%s)] TJ\n", left, mid, right)
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// TfChangesBody renders body text in F1 then switches to F2 for the
// trailing word. Caller supplies font keys via Fonts map.
func TfChangesBody(body, accent string) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf 100 700 Td")
	fmt.Fprintf(&b, "(%s) Tj /F2 12 Tf (%s) Tj\n", body, accent)
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// MarkedContentBody renders one BDC /MCID 5 region and one untagged
// region after EMC. Tests assert MCID=5 on the first run and 0 on
// the second.
func MarkedContentBody(tagged, untagged string) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "/P << /MCID 5 >> BDC")
	fmt.Fprintln(&b, "BT /F1 12 Tf 100 700 Td")
	fmt.Fprintf(&b, "(%s) Tj\n", tagged)
	fmt.Fprintln(&b, "ET")
	fmt.Fprintln(&b, "EMC")
	fmt.Fprintln(&b, "BT /F1 12 Tf 100 670 Td")
	fmt.Fprintf(&b, "(%s) Tj\n", untagged)
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// WriteFormXObjectPDF emits a 1-page PDF that references a Form
// XObject named /Fm1 via the Do operator. The page itself has no
// text-bearing operators; all "text" sits inside the Form XObject's
// content stream and should be DROPPED with a logged warning by
// T2's walker.
func WriteFormXObjectPDF(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	xRefTable, err := pdfcpu.CreateDemoXRef()
	if err != nil {
		return fmt.Errorf("create demo xref: %w", err)
	}
	rootDict, err := xRefTable.Catalog()
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}

	// Form XObject content stream: BT/Tj/ET inside the form. T2's
	// walker should NOT recurse into this — it should log+skip the
	// outer Do.
	formContent := []byte("BT /F1 12 Tf 0 0 Td (xobj-text) Tj ET\n")
	formStreamDict, err := xRefTable.NewStreamDictForBuf(formContent)
	if err != nil {
		return fmt.Errorf("new form stream dict: %w", err)
	}
	formStreamDict.Insert("Type", types.Name("XObject"))
	formStreamDict.Insert("Subtype", types.Name("Form"))
	formStreamDict.Insert("BBox", types.RectForFormat("Letter").Array())
	if err := formStreamDict.Encode(); err != nil {
		return fmt.Errorf("encode form stream: %w", err)
	}
	formIndRef, err := xRefTable.IndRefForNewObject(*formStreamDict)
	if err != nil {
		return fmt.Errorf("indref form: %w", err)
	}

	mediaBox := types.RectForFormat("Letter")
	page := model.Page{
		MediaBox: mediaBox,
		Fm: model.FontMap{
			"Helvetica": model.FontResource{Res: model.Resource{ID: "F1"}},
		},
		Buf: bytes.NewBufferString("q /Fm1 Do Q\n"),
	}
	if err := pdfcpu.AddPageTreeWithSamplePage(xRefTable, rootDict, page); err != nil {
		return fmt.Errorf("add page tree: %w", err)
	}
	if err := injectXObjectResource(xRefTable, "Fm1", *formIndRef); err != nil {
		return fmt.Errorf("inject xobject resource: %w", err)
	}
	if err := api.CreatePDFFile(xRefTable, dst, nil); err != nil {
		return fmt.Errorf("create pdf file: %w", err)
	}
	return nil
}

// injectXObjectResource walks the catalog -> Pages -> Kids[0] and
// adds /Resources/XObject/<key> = ir to the first page's dict.
func injectXObjectResource(xRefTable *model.XRefTable, key string, ir types.IndirectRef) error {
	rootDict, err := xRefTable.Catalog()
	if err != nil {
		return err
	}
	pagesObj, _ := rootDict.Find("Pages")
	pagesDict, err := xRefTable.DereferenceDict(pagesObj)
	if err != nil {
		return err
	}
	kidsObj, _ := pagesDict.Find("Kids")
	kidsArr, err := xRefTable.DereferenceArray(kidsObj)
	if err != nil {
		return err
	}
	if len(kidsArr) == 0 {
		return fmt.Errorf("page tree has no kids")
	}
	pageDict, err := xRefTable.DereferenceDict(kidsArr[0])
	if err != nil {
		return err
	}

	var resDict types.Dict
	if rObj, ok := pageDict.Find("Resources"); ok {
		resDict, err = xRefTable.DereferenceDict(rObj)
		if err != nil {
			return err
		}
	}
	if resDict == nil {
		resDict = types.Dict{}
	}
	xobjDict, _ := resDict.Find("XObject")
	if xobjDict == nil {
		xobjDict = types.Dict{}
	}
	xobjMap, ok := xobjDict.(types.Dict)
	if !ok {
		xobjMap = types.Dict{}
	}
	xobjMap[key] = ir
	resDict["XObject"] = xobjMap
	pageDict["Resources"] = resDict
	return nil
}
