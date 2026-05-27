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

// StructElemSpec describes one tagged-PDF structure element. The
// caller assembles a tree of StructElemSpecs whose leaves carry MCID
// integers; WriteTaggedPDF turns the tree into a /StructTreeRoot
// dict + every nested /StructElem dict.
//
// Type is the /S name (e.g. "Document", "H1", "P", "Figure"); empty
// is illegal (every structure element must have an /S entry).
//
// Children + MCIDs are mutually compatible — a leaf node typically
// has only MCIDs; a container typically has only Children. Mixing the
// two is legal in PDF and used by this fixture set's nested-emit
// scenarios.
//
// ActualText, when non-empty, emits /A << /ActualText (text) >>.
// Attrs entries flatten into /A as additional name/string pairs.
type StructElemSpec struct {
	Type       string
	Children   []StructElemSpec
	MCIDs      []int
	ActualText string
	Attrs      map[string]string
}

// TaggedPageSpec is the input to WriteTaggedPDF: the page-level
// resources + content body (which must contain matching BDC/EMC
// blocks for every leaf MCID in the structure tree) + the structure
// tree itself.
type TaggedPageSpec struct {
	Fonts      []FontSpec
	Body       string
	StructTree StructElemSpec
}

// WriteTaggedPDF emits a single-page tagged PDF synthesizing
// /StructTreeRoot from spec.StructTree. The page's content stream
// must contain matching BDC/EMC marked-content regions for every
// leaf MCID — caller responsibility.
//
// Implementation mirrors WriteFormXObjectPDF's pattern:
//  1. CreateDemoXRef + Catalog.
//  2. AddPageTreeWithSamplePage with the spec's content body.
//  3. Walk spec.StructTree; emit a /StructElem dict for each node,
//     allocating indirect refs in post-order so parents can carry
//     refs to their children.
//  4. Stamp /Pg references on each StructElem to point at the page's
//     indirect ref.
//  5. Insert /StructTreeRoot into the Catalog and /MarkInfo with
//     /Marked = true so pdfcpu's validation flags the PDF as Tagged.
func WriteTaggedPDF(dst string, spec TaggedPageSpec) error {
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
	mediaBox := types.RectForFormat("Letter")

	page := model.Page{
		MediaBox: mediaBox,
		Fm:       buildFontMap(spec.Fonts),
		Buf:      bytes.NewBufferString(spec.Body),
	}
	if err := pdfcpu.AddPageTreeWithSamplePage(xRefTable, rootDict, page); err != nil {
		return fmt.Errorf("add page tree: %w", err)
	}
	pageIndRef, err := firstPageIndRef(xRefTable, rootDict)
	if err != nil {
		return fmt.Errorf("locate first page: %w", err)
	}

	rootStructIR, err := buildStructTreeRoot(xRefTable, &spec.StructTree, *pageIndRef)
	if err != nil {
		return fmt.Errorf("build struct tree: %w", err)
	}
	rootDict.Insert("StructTreeRoot", *rootStructIR)

	markInfo := types.NewDict()
	markInfo.InsertBool("Marked", true)
	rootDict.Insert("MarkInfo", markInfo)

	if err := api.CreatePDFFile(xRefTable, dst, nil); err != nil {
		return fmt.Errorf("create pdf file: %w", err)
	}
	return nil
}

// WriteHybridPartialPDF emits a 1-page PDF with two BT/Tj/ET blocks:
//   - the first wrapped in BDC /MCID 1 (tagged with a /P element)
//   - the second outside any marked-content region (residue for
//     HybridFallback)
//
// The /StructTreeRoot references only the first MCID; the second
// block surfaces in the residue partition during HybridFallback.
//
// The tagged region uses MCID 1 (not 0): T2's content-stream walker
// treats MCID == 0 as "outside any marked-content region" — the
// untagged-residue contract — so a /MCID 0 BDC would silently make
// the tagged region invisible to the walker.
func WriteHybridPartialPDF(dst string) error {
	const body = "/P << /MCID 1 >> BDC\n" +
		"BT /F1 12 Tf 100 700 Td (tagged paragraph) Tj ET\n" +
		"EMC\n" +
		"BT /F1 12 Tf 100 600 Td (untagged paragraph) Tj ET\n"
	spec := TaggedPageSpec{
		Fonts: SimpleFontSpecMap(map[string]string{"F1": "Helvetica"}),
		Body:  body,
		StructTree: StructElemSpec{
			Type: "Document",
			Children: []StructElemSpec{
				{Type: "P", MCIDs: []int{1}},
			},
		},
	}
	return WriteTaggedPDF(dst, spec)
}

// buildStructTreeRoot allocates the /StructTreeRoot dict, walks the
// supplied root spec to build /StructElem indirect refs in post-order,
// and returns the root's indirect ref.
func buildStructTreeRoot(xRefTable *model.XRefTable, rootSpec *StructElemSpec, pageIR types.IndirectRef) (*types.IndirectRef, error) {
	rootDict := types.NewDict()
	rootDict.InsertName("Type", "StructTreeRoot")

	rootElemIR, err := buildStructElem(xRefTable, rootSpec, pageIR)
	if err != nil {
		return nil, err
	}
	rootDict.Insert("K", *rootElemIR)
	return xRefTable.IndRefForNewObject(rootDict)
}

// buildStructElem allocates one /StructElem dict (post-order: kids
// first) and returns its indirect ref. /K is an array combining
// child IndirectRefs and bare MCID Integers; /Pg points at pageIR;
// /A optionally carries /ActualText and Attrs.
func buildStructElem(xRefTable *model.XRefTable, spec *StructElemSpec, pageIR types.IndirectRef) (*types.IndirectRef, error) {
	if spec.Type == "" {
		return nil, fmt.Errorf("structure element missing /S type")
	}
	dict := types.NewDict()
	dict.InsertName("Type", "StructElem")
	dict.InsertName("S", spec.Type)
	dict.Insert("Pg", pageIR)

	kArr := make(types.Array, 0, len(spec.Children)+len(spec.MCIDs))
	for i := range spec.Children {
		childIR, err := buildStructElem(xRefTable, &spec.Children[i], pageIR)
		if err != nil {
			return nil, err
		}
		kArr = append(kArr, *childIR)
	}
	for _, mcid := range spec.MCIDs {
		kArr = append(kArr, types.Integer(mcid))
	}
	if len(kArr) == 1 {
		// PDF spec accepts a single object directly under /K; arrays
		// of length 1 also legal. Use the array form for uniformity.
	}
	if len(kArr) > 0 {
		dict.Insert("K", kArr)
	}
	if attrDict := buildAttributeDict(spec); attrDict != nil {
		dict.Insert("A", *attrDict)
	}
	return xRefTable.IndRefForNewObject(dict)
}

// buildAttributeDict returns a /A dict carrying ActualText and any
// extra Attrs. Returns nil when the spec has no attributes.
func buildAttributeDict(spec *StructElemSpec) *types.Dict {
	if spec.ActualText == "" && len(spec.Attrs) == 0 {
		return nil
	}
	d := types.NewDict()
	if spec.ActualText != "" {
		d.InsertString("ActualText", spec.ActualText)
	}
	for k, v := range spec.Attrs {
		d.InsertString(k, v)
	}
	return &d
}

// firstPageIndRef returns the indirect ref of the first page in the
// already-built page tree (used by WriteTaggedPDF to stamp /Pg
// entries onto every StructElem). Mirrors injectXObjectResource's
// catalog→Pages→Kids[0] walk in fixturelib.go.
func firstPageIndRef(xRefTable *model.XRefTable, rootDict types.Dict) (*types.IndirectRef, error) {
	pagesObj, _ := rootDict.Find("Pages")
	pagesDict, err := xRefTable.DereferenceDict(pagesObj)
	if err != nil {
		return nil, err
	}
	kidsObj, _ := pagesDict.Find("Kids")
	kidsArr, err := xRefTable.DereferenceArray(kidsObj)
	if err != nil {
		return nil, err
	}
	if len(kidsArr) == 0 {
		return nil, fmt.Errorf("page tree has no kids")
	}
	if ir, ok := kidsArr[0].(types.IndirectRef); ok {
		return &ir, nil
	}
	return nil, fmt.Errorf("first page kid is not an IndirectRef")
}

// buildFontMap converts a []FontSpec into pdfcpu's model.FontMap so
// AddPageTreeWithSamplePage can wire the page's /Resources/Font
// subdict. Each FontSpec's Key + BaseFont becomes a single /Font
// entry — this mirrors the pattern in WriteFormXObjectPDF.
func buildFontMap(fonts []FontSpec) model.FontMap {
	out := make(model.FontMap, len(fonts))
	for _, fs := range fonts {
		out[fs.BaseFont] = model.FontResource{Res: model.Resource{ID: fs.Key}}
	}
	return out
}
