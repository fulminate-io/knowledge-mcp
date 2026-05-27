//go:build ignore

// Command gen_t4 synthesizes the 4 T4 layout-clusterer integration
// fixtures used by collector/pdf/layout/integration_test.go.
// Mirrors the pattern in collector/pdf/testdata/gen.go.
//
// Run from collector/pdf/:
//
//	cd collector/pdf
//	go run ./testdata/gen_t4.go
//
// Output is committed to git; regeneration is explicit (re-run when
// the fixture content needs to evolve).
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
		log.Fatalf("gen_t4: getwd: %v", err)
	}
	base := filepath.Join(cwd, "testdata")
	helvF1 := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})

	fixtures := []struct {
		dest string
		spec fixturelib.PageSpec
	}{
		{
			dest: "t4_paragraph_simple.pdf",
			spec: fixturelib.PageSpec{Fonts: helvF1, Body: fixturelib.T4ParagraphSimpleBody()},
		},
		{
			dest: "t4_hyphenated_paragraph.pdf",
			spec: fixturelib.PageSpec{Fonts: helvF1, Body: fixturelib.T4HyphenatedParagraphBody()},
		},
		{
			dest: "t4_rotated90.pdf",
			spec: fixturelib.PageSpec{Fonts: helvF1, Body: fixturelib.T4Rotated90Body(), Rotation: 90},
		},
		{
			dest: "t4_mixed_font_paragraph.pdf",
			spec: fixturelib.PageSpec{Fonts: helvF1, Body: fixturelib.T4MixedFontParagraphBody()},
		},
	}
	for _, fx := range fixtures {
		dst := filepath.Join(base, fx.dest)
		if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{fx.spec}); err != nil {
			log.Fatalf("gen_t4: write %s: %v", dst, err)
		}
		st, err := os.Stat(dst)
		if err != nil {
			log.Fatalf("gen_t4: stat %s: %v", dst, err)
		}
		fmt.Printf("gen_t4: wrote %s (%d bytes)\n", dst, st.Size())
	}
}
