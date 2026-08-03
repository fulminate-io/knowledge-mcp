//go:build ignore

// Command gen synthesizes the PDF fixtures used by collector/pdf
// tests and writes them under collector/pdf/testdata and
// collector/pdf/internal/pdfcpu/testdata. The shared builder helpers
// live in collector/pdf/testdata/fixturelib (a real package, not
// build-tagged ignore — the testdata convention excludes it from
// production builds).
//
// Run from collector/pdf/:
//
//	cd collector/pdf
//	go run ./testdata/gen.go
//
// Output is committed to git; regeneration is explicit (re-run when
// pdfcpu's writer output format changes or fixture content needs to
// evolve).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	fixturelib "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
)

// fixtureGroup defines one fixture: its relative-to-testdata
// destinations + the page specs that build it. The first entry in
// `dests` is collector/pdf/testdata/<name>; for fixtures that
// internal/pdfcpu's tests also need, the second entry mirrors them
// under internal/pdfcpu/testdata.
type fixtureGroup struct {
	dests []string
	specs []fixturelib.PageSpec
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("gen: getwd: %v", err)
	}
	base := filepath.Join(cwd, "testdata")
	for _, fx := range fixtures() {
		for _, rel := range fx.dests {
			dst := filepath.Join(base, rel)
			if err := fixturelib.WritePDF(dst, fx.specs); err != nil {
				log.Fatalf("gen: write %s: %v", dst, err)
			}
			reportSize(dst)
		}
	}
	// Form-XObject fixture takes a special path because the wrapper
	// needs to inject XObject resources into the page dict AFTER
	// AddPageTreeWithSamplePage runs — fixturelib.WriteFormXObjectPDF
	// owns the full assembly.
	formDst := filepath.Join(base, "form_xobject.pdf")
	if err := fixturelib.WriteFormXObjectPDF(formDst); err != nil {
		log.Fatalf("gen: write %s: %v", formDst, err)
	}
	reportSize(formDst)

	// T4.6 fixture: Form XObject whose own /Resources/Font defines a
	// font key (T1_0) absent from the page-level Resources. Exercises
	// the FontResourcesHint plumbing on the resolver.
	formOwnFontsDst := filepath.Join(base, "form_xobject_own_fonts.pdf")
	if err := fixturelib.WriteFormXObjectOwnFontsPDF(formOwnFontsDst); err != nil {
		log.Fatalf("gen: write %s: %v", formOwnFontsDst, err)
	}
	reportSize(formOwnFontsDst)

	// T3 fixtures — 6 single-page strategy fixtures + 1 multi-page
	// caching fixture. See fixturelib/fontspec_t3fixtures.go for each
	// font's distinguishing field path.
	writeT3Fixtures(base)

	// T6 fixtures — 6 synthetic tagged-PDF fixtures driving the
	// /StructTreeRoot walker. See fixturelib/tagged.go for the
	// WriteTaggedPDF helper that synthesizes the structure tree.
	writeT6Fixtures(base)
}

// writeT3Fixtures emits the 7 T3 fixtures. Each single-page fixture
// exercises one rung of the resolver's strategy ladder; the multi-page
// fixture exercises the document-scoped cache.
func writeT3Fixtures(base string) {
	t3 := []struct {
		dest  string
		specs []fixturelib.PageSpec
	}{
		{
			dest: "tounicode_clean.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.ToUnicodeCleanFont()},
				Body:  fixturelib.ToUnicodeCleanBody(),
			}},
		},
		{
			dest: "no_tounicode_winansi.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.NoToUnicodeWinAnsiFont()},
				Body:  fixturelib.NoToUnicodeWinAnsiBody(),
			}},
		},
		{
			dest: "differences_override.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.DifferencesOverrideFont()},
				Body:  fixturelib.DifferencesOverrideBody(),
			}},
		},
		{
			dest: "no_encoding_info.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.NoEncodingInfoFont()},
				Body:  fixturelib.NoEncodingInfoBody(),
			}},
		},
		{
			dest: "ligatures.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.LigaturesFont()},
				Body:  fixturelib.LigaturesBody(),
			}},
		},
		{
			dest: "cidfont_identity_h.pdf",
			specs: []fixturelib.PageSpec{{
				Fonts: []fixturelib.FontSpec{fixturelib.CIDFontIdentityHFont()},
				Body:  fixturelib.CIDFontIdentityHBody(),
			}},
		},
	}
	for _, fx := range t3 {
		dst := filepath.Join(base, fx.dest)
		if err := fixturelib.WritePDF(dst, fx.specs); err != nil {
			log.Fatalf("gen: write %s: %v", dst, err)
		}
		reportSize(dst)
	}

	// multipage_one_font.pdf: 3 pages, one shared Helvetica F1 dict.
	mpDst := filepath.Join(base, "multipage_one_font.pdf")
	bodies := fixturelib.MultiPageOneFontBodies()
	specs := make([]fixturelib.PageSpec, 0, len(bodies))
	for _, body := range bodies {
		specs = append(specs, fixturelib.PageSpec{Body: body})
	}
	shared := []fixturelib.FontSpec{fixturelib.MultiPageOneFontShared()}
	if err := fixturelib.WriteMultiPagePDF(mpDst, specs, shared); err != nil {
		log.Fatalf("gen: write %s: %v", mpDst, err)
	}
	reportSize(mpDst)
}

// writeT6Fixtures emits the 6 tagged-PDF fixtures. Each fixture exercises
// one shape of the /StructTreeRoot walker; see
// collector/pdf/structtree/walk_test.go for the assertions.
func writeT6Fixtures(base string) {
	helvF1 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})

	// 1. simple_tagged.pdf — Document → [H1 (mcid 1), P (mcid 2), P (mcid 3)].
	simpleBody := "/H1 << /MCID 1 >> BDC\n" +
		"BT /F1 18 Tf 100 750 Td (Heading One) Tj ET\n" +
		"EMC\n" +
		"/P << /MCID 2 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (First paragraph.) Tj ET\n" +
		"EMC\n" +
		"/P << /MCID 3 >> BDC\n" +
		"BT /F1 12 Tf 100 700 Td (Second paragraph.) Tj ET\n" +
		"EMC\n"
	writeTagged(base, "simple_tagged.pdf", fixturelib.TaggedPageSpec{
		Fonts: helvF1,
		Body:  simpleBody,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{
				{Type: "H1", MCIDs: []int{1}},
				{Type: "P", MCIDs: []int{2}},
				{Type: "P", MCIDs: []int{3}},
			},
		},
	})

	// 2. nested_tagged.pdf — Document → Part → Sect → H2 → P.
	nestedBody := "/H2 << /MCID 1 >> BDC\n" +
		"BT /F1 16 Tf 100 750 Td (Section Title) Tj ET\n" +
		"EMC\n" +
		"/P << /MCID 2 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (Section body prose.) Tj ET\n" +
		"EMC\n"
	writeTagged(base, "nested_tagged.pdf", fixturelib.TaggedPageSpec{
		Fonts: helvF1,
		Body:  nestedBody,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{{Type: "Part", Children: []fixturelib.StructElemSpec{
				{Type: "Sect", Children: []fixturelib.StructElemSpec{
					{Type: "H2", MCIDs: []int{1}},
					{Type: "P", MCIDs: []int{2}},
				}},
			}}},
		},
	})

	// 3. list_tagged.pdf — Document → L → [LI (mcid 1), LI (mcid 2)].
	listBody := "/LI << /MCID 1 >> BDC\n" +
		"BT /F1 12 Tf 100 750 Td (First item) Tj ET\n" +
		"EMC\n" +
		"/LI << /MCID 2 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (Second item) Tj ET\n" +
		"EMC\n"
	writeTagged(base, "list_tagged.pdf", fixturelib.TaggedPageSpec{
		Fonts: helvF1,
		Body:  listBody,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{{Type: "L", Children: []fixturelib.StructElemSpec{
				{Type: "LI", MCIDs: []int{1}},
				{Type: "LI", MCIDs: []int{2}},
			}}},
		},
	})

	// 4. actualtext_tagged.pdf — Document → P (mcid 1) with /ActualText.
	actualBody := "/P << /MCID 1 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (original text) Tj ET\n" +
		"EMC\n"
	writeTagged(base, "actualtext_tagged.pdf", fixturelib.TaggedPageSpec{
		Fonts: helvF1,
		Body:  actualBody,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{
				{Type: "P", MCIDs: []int{1}, ActualText: "replaced text"},
			},
		},
	})

	// 5. vendor_tagged.pdf — Document → MyCorp::Custom → P.
	vendorBody := "/P << /MCID 1 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (Custom paragraph.) Tj ET\n" +
		"EMC\n"
	writeTagged(base, "vendor_tagged.pdf", fixturelib.TaggedPageSpec{
		Fonts: helvF1,
		Body:  vendorBody,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{{Type: "MyCorp::Custom", Children: []fixturelib.StructElemSpec{
				{Type: "P", MCIDs: []int{1}},
			}}},
		},
	})

	// 6. hybrid_partial.pdf — convenience wrapper.
	hybridDst := filepath.Join(base, "hybrid_partial.pdf")
	if err := fixturelib.WriteHybridPartialPDF(hybridDst); err != nil {
		log.Fatalf("gen: write %s: %v", hybridDst, err)
	}
	reportSize(hybridDst)
}

// writeTagged is the per-fixture orchestration helper for T6: it
// resolves the destination path, calls fixturelib.WriteTaggedPDF, and
// reports the resulting file size.
func writeTagged(base, name string, spec fixturelib.TaggedPageSpec) {
	dst := filepath.Join(base, name)
	if err := fixturelib.WriteTaggedPDF(dst, spec); err != nil {
		log.Fatalf("gen: write %s: %v", dst, err)
	}
	reportSize(dst)
}

func reportSize(dst string) {
	st, err := os.Stat(dst)
	if err != nil {
		log.Fatalf("gen: stat %s: %v", dst, err)
	}
	fmt.Printf("gen: wrote %s (%d bytes)\n", dst, st.Size())
}

// fixtures returns every (name, page-specs) pair this generator emits.
// The list grows as new tickets need new fixtures.
func fixtures() []fixtureGroup {
	helvF1 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})
	helvBoldF2 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica", "F2": "Helvetica-Bold"})
	return []fixtureGroup{
		{
			dests: []string{"onepage.pdf", "../internal/pdfcpu/testdata/onepage.pdf"},
			specs: []fixturelib.PageSpec{
				{Fonts: helvF1, Body: fixturelib.OnePagePDFBody("Hello, T1")},
			},
		},
		{
			dests: []string{"paragraph.pdf"},
			specs: []fixturelib.PageSpec{
				{Fonts: helvF1, Body: fixturelib.ParagraphBody(
					"first line", "second line", "third line")},
			},
		},
		{
			dests: []string{"tj_kerning.pdf"},
			specs: []fixturelib.PageSpec{
				{Fonts: helvF1, Body: fixturelib.TJKerningBody("Hel", "lo", "world")},
			},
		},
		{
			dests: []string{"tf_changes.pdf"},
			specs: []fixturelib.PageSpec{
				{Fonts: helvBoldF2, Body: fixturelib.TfChangesBody("plain ", "BOLD")},
			},
		},
		{
			dests: []string{"marked_content.pdf"},
			specs: []fixturelib.PageSpec{
				{Fonts: helvF1, Body: fixturelib.MarkedContentBody("tagged", "untagged")},
			},
		},
	}
}
