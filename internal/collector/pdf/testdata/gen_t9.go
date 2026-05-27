//go:build ignore

// Command gen_t9 synthesizes the 3 T9 Phase 5 synthetic-content
// fixtures (multipage-cross-paragraph, list-heavy, code-heavy) under
// collector/pdf/testdata/corpus/<fixture-name>/source.pdf. Mirrors
// the pattern in collector/pdf/testdata/gen.go and gen_t4.go.
//
// Run from collector/pdf/:
//
//	cd collector/pdf
//	go run ./testdata/gen_t9.go
//
// Output is committed to git; regeneration is explicit. After
// regen, re-run the chunker against each fixture and refresh
// chunks.golden.json via the project's golden-dump tooling (see
// collector/pdf/testdata/CONTRIBUTING.md).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	fixturelib "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("gen_t9: getwd: %v", err)
	}
	corpus := filepath.Join(cwd, "testdata", "corpus")
	helvF1 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})
	courF1 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica", "F2": "Courier"})

	if err := writeMultipageCrossParagraph(corpus, helvF1); err != nil {
		log.Fatalf("gen_t9: multipage-cross-paragraph: %v", err)
	}
	if err := writeListHeavy(corpus, helvF1); err != nil {
		log.Fatalf("gen_t9: list-heavy: %v", err)
	}
	if err := writeCodeHeavy(corpus, courF1); err != nil {
		log.Fatalf("gen_t9: code-heavy: %v", err)
	}
}

// writeMultipageCrossParagraph emits a 3-page PDF where a single
// flowing paragraph spans the page-2 / page-3 boundary. Each page
// shares the F1 (Helvetica) indirect ref via WriteMultiPagePDF's
// shared-font path.
func writeMultipageCrossParagraph(corpus string, helvF1 []fixturelib.FontSpec) error {
	dst := filepath.Join(corpus, "multipage-cross-paragraph", "source.pdf")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	pages := []fixturelib.PageSpec{
		{Fonts: helvF1, Body: bodyHelloPage1()},
		{Fonts: helvF1, Body: bodyContinuingParagraphTopOfPage2()},
		{Fonts: helvF1, Body: bodyContinuingParagraphTopOfPage3()},
	}
	if err := fixturelib.WriteMultiPagePDF(dst, pages, helvF1); err != nil {
		return err
	}
	return reportSize(dst)
}

func bodyHelloPage1() string {
	return "BT /F1 12 Tf 14 TL 100 720 Td " +
		"(This document spans three pages and exercises) Tj T* " +
		"(the chunker's cross-page paragraph continuity logic.) Tj T* " +
		"(The next page begins mid-sentence, continuing) Tj T* " +
		"(this paragraph without an intervening blank line.) Tj " +
		"ET\n"
}

func bodyContinuingParagraphTopOfPage2() string {
	return "BT /F1 12 Tf 14 TL 100 720 Td " +
		"(The chunker should recognize this run as the same) Tj T* " +
		"(paragraph that began on page 1, joining the spans) Tj T* " +
		"(across the page boundary into one logical chunk.) Tj " +
		"ET\n"
}

func bodyContinuingParagraphTopOfPage3() string {
	return "BT /F1 12 Tf 14 TL 100 720 Td " +
		"(And the final continuation here on page 3 closes) Tj T* " +
		"(the cross-page paragraph fixture.) Tj " +
		"ET\n"
}

// writeListHeavy emits a 1-page PDF with three list types (bullet,
// numbered, nested). Bullet/number prefixes are emitted as ASCII
// glyphs (* and 1./2./3.) — the classifier's list heuristic operates
// on text content rather than typographic markers.
func writeListHeavy(corpus string, helvF1 []fixturelib.FontSpec) error {
	dst := filepath.Join(corpus, "list-heavy", "source.pdf")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	body := "BT /F1 12 Tf 14 TL 100 740 Td " +
		"(* first bullet item) Tj T* " +
		"(* second bullet item) Tj T* " +
		"(* third bullet item) Tj T* " +
		"(* fourth bullet item) Tj T* " +
		"() Tj T* " +
		"(1. ordered item one) Tj T* " +
		"(2. ordered item two) Tj T* " +
		"(3. ordered item three) Tj T* " +
		"() Tj T* " +
		"(- outer item) Tj T* " +
		"(  - inner one) Tj T* " +
		"(  - inner two) Tj T* " +
		"(- second outer item) Tj " +
		"ET\n"
	if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{{Fonts: helvF1, Body: body}}); err != nil {
		return err
	}
	return reportSize(dst)
}

// writeCodeHeavy emits a 1-page PDF alternating prose paragraphs with
// monospace-font code blocks. Two F-keys: F1 (Helvetica) for prose,
// F2 (Courier) for code. The classifier's code-block heuristic keys
// on monospace ratio.
func writeCodeHeavy(corpus string, fonts []fixturelib.FontSpec) error {
	dst := filepath.Join(corpus, "code-heavy", "source.pdf")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	body := "BT /F1 12 Tf 14 TL 100 740 Td " +
		"(Below is a Go function example demonstrating) Tj T* " +
		"(the standard error-handling pattern in idiomatic Go.) Tj T* " +
		"() Tj T* " +
		"ET\n" +
		"BT /F2 11 Tf 13 TL 100 680 Td " +
		"(func Example()) Tj T* " +
		"(    if err != nil {) Tj T* " +
		"(        return fmt.Errorf) Tj T* " +
		"(    }) Tj T* " +
		"() Tj T* " +
		"ET\n" +
		"BT /F1 12 Tf 14 TL 100 600 Td " +
		"(After the code block, prose resumes with) Tj T* " +
		"(a closing remark on the example above.) Tj " +
		"ET\n"
	if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{{Fonts: fonts, Body: body}}); err != nil {
		return err
	}
	return reportSize(dst)
}

func reportSize(dst string) error {
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Printf("gen_t9: wrote %s (%d bytes)\n", dst, st.Size())
	return nil
}
