//go:build ignore

// Command regen_widths regenerates collector/pdf/font/standard14_widths.dat.
// The .dat is a TSV (font, glyph, width) snapshot of every Standard 14
// font × every AdobeGlyphList glyph that pdfcpu's public API (font.CharWidth)
// reports as having a non-zero advance width. The .dat is a GENERATED
// artifact: re-run when pdfcpu's vendored AFM tables change.
//
// Run from collector/pdf/font:
//
//	cd collector/pdf/font
//	go run ./testdata/regen_widths.go
//
// The script writes the .dat to TWO locations as a single atomic step:
//
//  1. collector/pdf/font/standard14_widths.dat                  (runtime embed)
//  2. collector/pdf/testdata/fixturelib/widths_data/standard14_widths.dat (fixturelib embed)
//
// Writing both at once preserves single-sourcing: the two embed targets
// share the same byte sequence by construction, not by hand-copy. Re-run
// the regenerator whenever pdfcpu's vendored AFM tables change or AGL
// updates (rare). Symlinks would be cleaner but `go:embed` rejects
// irregular files, so byte-identical copies are the constraint.
//
// The AGL source is read directly from collector/pdf/font/glyphlist.txt
// (the runtime path embeds the same file). Reading from disk rather than
// importing the font package keeps this regenerator independent of the
// font/ package's compilation state — the regenerator must work even
// when font/ has compile errors mid-implementation.
//
// Output is deterministic: glyph names sorted alphabetically per font.
// Two consecutive runs produce byte-identical output unless pdfcpu's
// tables or the upstream AGL change.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/font"
)

// standard14 lists the 14 Standard PostScript names (PDF 32000-1:2008 §9.6.2.2).
// Order matches the spec table; output is grouped by this order.
var standard14 = []string{
	"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
	"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
	"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
	"Symbol", "ZapfDingbats",
}

// header is the .dat preamble. Cites the public API surface (pdfcpu.CharWidth)
// per Requirement C′ (revision 4) — the internal/corefont/metrics path is
// mentioned for context but is Go-internal and unimportable.
var header = []string{
	"# Source: pdfcpu@v0.12.0 pdfcpu.CharWidth() public API (pkg/font/metrics.go:303 — github.com/pdfcpu/pdfcpu/pkg/font)",
	"# Backed by github.com/pdfcpu/pdfcpu/internal/corefont/metrics — files: gen.go, metrics.go, standard.go (Go-internal, unimportable)",
	"# Generated via: go run ./testdata/regen_widths.go > standard14_widths.dat (calls font.CharWidth for every Standard 14 font × every AGL glyph)",
	"# Spec reference: PDF 32000-1:2008 §9.6.2.2 (Standard Type 1 Fonts) — readers SHALL use AFM widths when /Widths absent.",
	"# Field order: font_name<TAB>glyph_name<TAB>advance_width (units = 1/1000 em)",
}

// outputPaths are the two embed targets, relative to the current working
// directory (which the script's docstring requires to be collector/pdf/font).
// Both paths receive byte-identical content.
var outputPaths = []string{
	"standard14_widths.dat",
	filepath.Join("..", "testdata", "fixturelib", "widths_data", "standard14_widths.dat"),
}

func main() {
	aglPath := "glyphlist.txt"
	agl, err := readAGL(aglPath)
	if err != nil {
		log.Fatalf("regen_widths: read AGL %q: %v", aglPath, err)
	}

	var buf bytes.Buffer
	out := bufio.NewWriter(&buf)
	for _, line := range header {
		fmt.Fprintln(out, line)
	}

	// Sorted glyph-name slice — deterministic output across runs.
	names := make([]string, 0, len(agl))
	for name := range agl {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, fontName := range standard14 {
		if !font.IsCoreFont(fontName) {
			fmt.Fprintf(os.Stderr, "regen_widths: WARN: pdfcpu does not classify %q as a core font; skipping\n", fontName)
			continue
		}
		for _, name := range names {
			runes := agl[name]
			if len(runes) == 0 {
				continue
			}
			// Use the FIRST rune for ligature glyph names — pdfcpu's
			// CharWidth takes a single rune, and ligature glyph metrics
			// are font-author-defined so the multi-rune mapping isn't
			// directly relevant for width lookup. Single-glyph entries
			// dominate the AGL anyway.
			width := font.CharWidth(fontName, runes[0])
			if width == 0 {
				continue // skip glyphs the font doesn't contain
			}
			fmt.Fprintf(out, "%s\t%s\t%d\n", fontName, name, width)
		}
	}
	if err := out.Flush(); err != nil {
		log.Fatalf("regen_widths: flush buffer: %v", err)
	}

	bb := buf.Bytes()
	for _, p := range outputPaths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.Fatalf("regen_widths: mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, bb, 0o644); err != nil { //nolint:gosec // committed test data
			log.Fatalf("regen_widths: write %s: %v", p, err)
		}
		fmt.Fprintf(os.Stderr, "regen_widths: wrote %s (%d bytes)\n", p, len(bb))
	}
}

// readAGL parses collector/pdf/font/glyphlist.txt into a glyph-name → []rune
// map. Comment lines (starting with '#') and blanks are skipped. Lines with
// malformed hex are reported on stderr and skipped (they are fatal in the
// runtime parser; this regenerator is more forgiving so a stray new entry
// doesn't block regeneration).
func readAGL(path string) (map[string][]rune, error) {
	f, err := os.Open(path) //nolint:gosec // path is the in-tree fixture
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string][]rune, 4400)
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ";", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "regen_widths: AGL line %d malformed (no ';'): %q\n", lineNo, line)
			continue
		}
		name := parts[0]
		hexes := strings.Fields(parts[1])
		runes := make([]rune, 0, len(hexes))
		for _, h := range hexes {
			v, err := strconv.ParseUint(h, 16, 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "regen_widths: AGL line %d bad hex %q: %v\n", lineNo, h, err)
				continue
			}
			runes = append(runes, rune(v))
		}
		if len(runes) > 0 {
			out[name] = runes
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
