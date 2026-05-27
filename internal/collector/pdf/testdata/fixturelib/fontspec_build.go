package fixturelib

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// WriteMultiPagePDF is the multi-page fixture entry point. Each
// PageSpec describes one page's content stream + per-page font slice;
// when sharedFonts is non-nil, those FontSpecs are emitted ONCE as
// indirect refs and every page's /Resources/Font subdict points at
// the same dict. This is required by multipage_one_font.pdf (the
// caching regression fixture per T3-1): three pages must resolve to
// the same Helvetica F1 indirect-ref so the document-scoped resolver
// hits its cache.
//
// Single-page fixtures use WritePDF (kept for the legacy call shape;
// it now thin-wraps this).
//
// The implementation bypasses pdfcpu's AddPageTreeWithSamplePage
// (which only handles single-page trees) and assembles the page tree
// manually so multi-page fixtures and shared fonts both work.
func WriteMultiPagePDF(dst string, specs []PageSpec, sharedFonts []FontSpec) error {
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

	// Pre-register shared font indirect refs (when supplied). When
	// sharedFonts is empty, each page registers its own per-page
	// fonts inline.
	sharedRefs, err := registerFonts(xRefTable, sharedFonts)
	if err != nil {
		return fmt.Errorf("register shared fonts: %w", err)
	}

	// Allocate the parent /Pages dict first so each child page can
	// reference its parent indref.
	parentDict := types.NewDict()
	parentDict.InsertName("Type", "Pages")
	parentRef, err := xRefTable.IndRefForNewObject(parentDict)
	if err != nil {
		return fmt.Errorf("indref parent /Pages: %w", err)
	}
	pageRefs := make([]types.IndirectRef, 0, len(specs))
	for i, spec := range specs {
		ir, err := buildPageDict(xRefTable, mediaBox, spec, sharedRefs, *parentRef)
		if err != nil {
			return fmt.Errorf("build page %d: %w", i, err)
		}
		pageRefs = append(pageRefs, *ir)
	}
	parentDict.Insert("Count", types.Integer(len(pageRefs)))
	kids := make(types.Array, 0, len(pageRefs))
	for _, r := range pageRefs {
		kids = append(kids, r)
	}
	parentDict.Insert("Kids", kids)
	parentDict.Insert("MediaBox", mediaBox.Array())
	rootDict.Insert("Pages", *parentRef)

	if err := api.CreatePDFFile(xRefTable, dst, nil); err != nil {
		return fmt.Errorf("create pdf file: %w", err)
	}
	return nil
}

// registerFonts emits each FontSpec as an indirect-ref'd font dict on
// xRefTable and returns key→indRef. Empty input returns an empty map.
func registerFonts(xRefTable *model.XRefTable, fonts []FontSpec) (map[string]types.IndirectRef, error) {
	out := make(map[string]types.IndirectRef, len(fonts))
	for _, fs := range fonts {
		ir, err := buildFontDict(xRefTable, fs)
		if err != nil {
			return nil, fmt.Errorf("buildFontDict(%q): %w", fs.Key, err)
		}
		out[fs.Key] = *ir
	}
	return out, nil
}

// buildPageDict assembles a single page dict with the supplied shared
// fonts and per-page content stream, returns its indirect ref. Used
// inside the manual page-tree assembly loop.
func buildPageDict(xRefTable *model.XRefTable, mediaBox *types.Rectangle, spec PageSpec, sharedRefs map[string]types.IndirectRef, parent types.IndirectRef) (*types.IndirectRef, error) {
	pageDict := types.NewDict()
	pageDict.InsertName("Type", "Page")
	pageDict.Insert("Parent", parent)
	pageDict.Insert("MediaBox", mediaBox.Array())
	if spec.Rotation != 0 {
		pageDict.InsertInt("Rotate", spec.Rotation)
	}

	// Resolve per-page font references: shared first, then per-page
	// FontSpec entries declared on the PageSpec.
	fontDict := types.NewDict()
	for k, ref := range sharedRefs {
		fontDict.Insert(k, ref)
	}
	for _, fs := range spec.Fonts {
		// Skip duplicates already covered by sharedRefs.
		if _, ok := sharedRefs[fs.Key]; ok {
			continue
		}
		ir, err := buildFontDict(xRefTable, fs)
		if err != nil {
			return nil, fmt.Errorf("build per-page font %q: %w", fs.Key, err)
		}
		fontDict.Insert(fs.Key, *ir)
	}
	if len(fontDict) > 0 {
		resDict := types.NewDict()
		resDict.Insert("Font", fontDict)
		pageDict.Insert("Resources", resDict)
	}

	contentBytes := []byte(spec.Body)
	contentSD, err := xRefTable.NewStreamDictForBuf(contentBytes)
	if err != nil {
		return nil, fmt.Errorf("new content stream: %w", err)
	}
	if err := contentSD.Encode(); err != nil {
		return nil, fmt.Errorf("encode content stream: %w", err)
	}
	contentIR, err := xRefTable.IndRefForNewObject(*contentSD)
	if err != nil {
		return nil, fmt.Errorf("indref content stream: %w", err)
	}
	pageDict.Insert("Contents", *contentIR)
	return xRefTable.IndRefForNewObject(pageDict)
}

// buildFontDict assembles a font dict for a FontSpec and returns its
// indirect ref. For now this only emits the simple-font shape:
// /Type /Font /Subtype /BaseFont /Encoding /FirstChar /LastChar
