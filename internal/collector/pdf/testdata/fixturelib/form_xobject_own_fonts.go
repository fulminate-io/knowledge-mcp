package fixturelib

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// WriteFormXObjectOwnFontsPDF emits a 1-page PDF that exercises the
// Form-XObject-owns-its-Resources path (T4.6 ticket FUL-85). Layout:
//
//   - Page /Resources/Font:    F1   -> Helvetica       with /ToUnicode
//     so the byte codes for the literal "page-text" decode cleanly
//     to that ASCII string.
//   - Form X1 /Resources/Font: T1_0 -> Helvetica-Bold  with /ToUnicode
//     so the byte codes for the literal "xobj-text" decode cleanly
//     to that ASCII string.
//   - Page content stream:     BT /F1   12 Tf 100 700 Td (page-text) Tj ET
//     followed by              q /Fm1 Do Q
//   - Form X1 content stream:  BT /T1_0 12 Tf 0   0   Td (xobj-text) Tj ET
//
// Load-bearing invariant: T1_0 MUST NOT appear in the page's
// /Resources/Font subdict — only inside the Form XObject's own
// /Resources. A page-level lookup of T1_0 must miss; a Form-context
// lookup must hit. This is the precondition the T4.6 resolver
// threading is meant to satisfy.
//
// The fixture goes through assembleOwnFontsPDF rather than
// WriteFormXObjectPDF (the T4.5 helper) because that helper does not
// support attaching a /Resources dict to the Form's stream dict.
func WriteFormXObjectOwnFontsPDF(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	return assembleOwnFontsPDF(dst)
}

// asciiToUnicodeCMap returns a /ToUnicode CMap mapping every byte in
// s to its ASCII Unicode codepoint. Stable wrapper over
// minimalToUnicodeCMap so the per-fixture body stays readable.
func asciiToUnicodeCMap(s string) []byte {
	pairs := make(map[string]string, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		pairs[fmt.Sprintf("%02X", c)] = fmt.Sprintf("%04X", c)
	}
	return minimalToUnicodeCMap(false, pairs)
}

// pageOwnFontSpec is the FontSpec for the page-level F1: Helvetica with
// a /ToUnicode CMap covering every byte of the literal page text.
func pageOwnFontSpec() FontSpec {
	spec := SimpleFontSpec("F1", "Helvetica")
	spec.ToUnicodeBytes = asciiToUnicodeCMap("page-text")
	return spec
}

// formOwnFontSpec is the FontSpec for the Form-level T1_0:
// Helvetica-Bold with a /ToUnicode CMap covering every byte of the
// literal Form text. Distinct font + distinct key + distinct text so
// the test can tell which Resources dict was consulted.
func formOwnFontSpec() FontSpec {
	spec := SimpleFontSpec("T1_0", "Helvetica-Bold")
	spec.ToUnicodeBytes = asciiToUnicodeCMap("xobj-text")
	return spec
}

// assembleOwnFontsPDF assembles the form_xobject_own_fonts.pdf fixture
// using the same manual page-tree construction WriteMultiPagePDF uses
// (so we can attach /Resources to the Form's stream dict directly,
// rather than relying on the page-level inheritance fall-through that
// pdfcpu's AddPageTreeWithSamplePage assumes).
func assembleOwnFontsPDF(dst string) error {
	xRefTable, err := pdfcpu.CreateDemoXRef()
	if err != nil {
		return fmt.Errorf("create demo xref: %w", err)
	}
	rootDict, err := xRefTable.Catalog()
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	mediaBox := types.RectForFormat("Letter")

	// 1. Build the page-level F1 (Helvetica) font dict.
	pageFontIR, err := buildFontDict(xRefTable, pageOwnFontSpec())
	if err != nil {
		return fmt.Errorf("build page F1: %w", err)
	}

	// 2. Build the Form-level T1_0 (Helvetica-Bold) font dict.
	formFontIR, err := buildFontDict(xRefTable, formOwnFontSpec())
	if err != nil {
		return fmt.Errorf("build form T1_0: %w", err)
	}

	// 3. Assemble the Form XObject. Its /Resources/Font ONLY carries
	//    T1_0 — page-level F1 is intentionally absent so the resolver
	//    must consult the Form's Resources to map the run.
	formContent := []byte("BT /T1_0 12 Tf 0 0 Td (xobj-text) Tj ET\n")
	formStreamDict, err := xRefTable.NewStreamDictForBuf(formContent)
	if err != nil {
		return fmt.Errorf("new form stream dict: %w", err)
	}
	formStreamDict.Insert("Type", types.Name("XObject"))
	formStreamDict.Insert("Subtype", types.Name("Form"))
	formStreamDict.Insert("BBox", mediaBox.Array())
	formResDict := types.NewDict()
	formFontDict := types.NewDict()
	formFontDict.Insert("T1_0", *formFontIR)
	formResDict.Insert("Font", formFontDict)
	formStreamDict.Insert("Resources", formResDict)
	if err := formStreamDict.Encode(); err != nil {
		return fmt.Errorf("encode form stream: %w", err)
	}
	formIR, err := xRefTable.IndRefForNewObject(*formStreamDict)
	if err != nil {
		return fmt.Errorf("indref form: %w", err)
	}

	// 4. Assemble the page. /Resources/Font carries ONLY F1; the page
	//    body emits a (page-text) Tj under F1, then q /Fm1 Do Q to
	//    invoke the Form (whose own Resources scopes T1_0).
	pageDict := types.NewDict()
	pageDict.InsertName("Type", "Page")
	pageDict.Insert("MediaBox", mediaBox.Array())
	pageRes := types.NewDict()
	pageFontMap := types.NewDict()
	pageFontMap.Insert("F1", *pageFontIR)
	pageRes.Insert("Font", pageFontMap)
	pageXObjMap := types.NewDict()
	pageXObjMap.Insert("Fm1", *formIR)
	pageRes.Insert("XObject", pageXObjMap)
	pageDict.Insert("Resources", pageRes)

	pageBody := bytes.NewBufferString("BT /F1 12 Tf 100 700 Td (page-text) Tj ET\nq /Fm1 Do Q\n")
	contentSD, err := xRefTable.NewStreamDictForBuf(pageBody.Bytes())
	if err != nil {
		return fmt.Errorf("new content stream: %w", err)
	}
	if err := contentSD.Encode(); err != nil {
		return fmt.Errorf("encode content stream: %w", err)
	}
	contentIR, err := xRefTable.IndRefForNewObject(*contentSD)
	if err != nil {
		return fmt.Errorf("indref content stream: %w", err)
	}
	pageDict.Insert("Contents", *contentIR)

	// 5. Page tree.
	parentDict := types.NewDict()
	parentDict.InsertName("Type", "Pages")
	parentRef, err := xRefTable.IndRefForNewObject(parentDict)
	if err != nil {
		return fmt.Errorf("indref parent /Pages: %w", err)
	}
	pageDict.Insert("Parent", *parentRef)
	pageRef, err := xRefTable.IndRefForNewObject(pageDict)
	if err != nil {
		return fmt.Errorf("indref page: %w", err)
	}
	parentDict.Insert("Count", types.Integer(1))
	parentDict.Insert("Kids", types.Array{*pageRef})
	parentDict.Insert("MediaBox", mediaBox.Array())
	rootDict.Insert("Pages", *parentRef)

	if err := api.CreatePDFFile(xRefTable, dst, nil); err != nil {
		return fmt.Errorf("create pdf file: %w", err)
	}
	return nil
}
