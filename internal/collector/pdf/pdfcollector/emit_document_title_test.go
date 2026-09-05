// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// emit_document_title_test.go — what the document root's SymbolName carries,
// and where that value came from.
//
// It is a separate file from emit_test.go because that file is at 481 lines
// against this repo's hard 500-line ceiling, not because the subject differs.
//
// Every leg drives the production collector over a real fixture, so the
// derivation and the emitter are checked against each other rather than
// against one author's belief about either.

// headlessFixture carries neither an Info-dict Title nor a top-level section:
// its single top-level chunk is the untagged synthetic wrapper block, which
// classifies as "block" and holds no text of its own. It is therefore the only
// checked-in shape that reaches the filename leg.
const headlessFixture = "../testdata/paragraph.pdf"

// titleSourceVocabulary is the CLOSED set metadata.title_source may carry,
// written down once here and cited from the placement assertion. It mirrors
// how raw_signals_test.go pins the schema stamp to the root and nothing else:
// the point of the key is that a consumer can branch on it, which requires
// both that every value is in the set and that no other node carries one.
var titleSourceVocabulary = map[string]bool{
	"info_dict":     true,
	"first_heading": true,
	"filename":      true,
}

// TestEmit_DocumentTitleDerivationAndProvenance observes the three-leg title
// fallback and the provenance stamp that reports which leg answered.
func TestEmit_DocumentTitleDerivationAndProvenance(t *testing.T) {
	t.Parallel()

	t.Run("info_dict", func(t *testing.T) {
		t.Parallel()
		// EXTERNAL EXPECTATION: the wanted title is read from the PDF's own
		// Info dict rather than from the emitter, so the subject does not
		// supply its own answer key.
		abs, err := filepath.Abs(rfcFixture)
		if err != nil {
			t.Fatalf("resolve fixture: %v", err)
		}
		doc, err := pdf.OpenFile(abs)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		want := doc.Metadata().Title
		doc.Close()
		if want == "" {
			t.Fatal("the rfc-7234-caching Info dict carries no Title; this fixture cannot witness the info_dict leg")
		}

		root := documentRootOf(t, collectFixture(t, rfcFixture))
		if got := root.GetSymbolName(); got != want {
			t.Errorf("document root SymbolName = %q, want the Info-dict Title %q byte-identically", got, want)
		}
		if got := root.GetMetadata()["title_source"]; got != "info_dict" {
			t.Errorf("title_source = %q, want %q", got, "info_dict")
		}
		// The Info-dict RECORD is untouched on the leg that read it.
		if got := root.GetMetadata()["title"]; got != want {
			t.Errorf("metadata title = %q, want the Info-dict record %q", got, want)
		}
	})

	t.Run("first_heading", func(t *testing.T) {
		t.Parallel()
		abs, err := filepath.Abs(taggedFixture)
		if err != nil {
			t.Fatalf("resolve fixture: %v", err)
		}
		doc, err := pdf.OpenFile(abs)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		infoTitle := doc.Metadata().Title
		doc.Close()
		if infoTitle != "" {
			t.Fatalf("the tagged fixture's Info dict carries the Title %q; this leg needs a TITLELESS document or it silently witnesses the info_dict leg instead", infoTitle)
		}

		root := documentRootOf(t, collectFixture(t, taggedFixture))
		if got := root.GetSymbolName(); got != "Heading One" {
			t.Errorf("document root SymbolName = %q, want the first top-level heading %q", got, "Heading One")
		}
		if got := root.GetMetadata()["title_source"]; got != "first_heading" {
			t.Errorf("title_source = %q, want %q", got, "first_heading")
		}
		// The blurb opens on the derived title; this fixture carries no author
		// and no subject, so the blurb is the title alone.
		if got := root.GetContent(); got != "Heading One" {
			t.Errorf("document root Content = %q, want the blurb to open on the derived title %q", got, "Heading One")
		}
		// ABSENCE LEG. Writing the derived title into metadata.title compiles
		// and passes the emitter, recipe and tools suites unchanged, so this
		// leg is the only thing standing between that variant and a shipped
		// graph in which a fabricated document property is indistinguishable
		// from one the Info dictionary really stated.
		if got, ok := root.GetMetadata()["title"]; ok {
			t.Errorf("metadata title = %q on a document whose Info dict carries none; the derived title belongs in SymbolName, not in the Info-dict record", got)
		}
	})

	t.Run("filename", func(t *testing.T) {
		t.Parallel()
		// The chosen name is the DISCRIMINATOR: mixed case and spaces. An
		// implementation reaching for SourceSlug or sanitizeSlug
		// (collector.go:128,141) returns "quarterly-report-2026" — right for a
		// graph name, and a destroyed human title.
		src, err := filepath.Abs(headlessFixture)
		if err != nil {
			t.Fatalf("resolve fixture: %v", err)
		}
		in, err := os.Open(src)
		if err != nil {
			t.Fatalf("open %s: %v", src, err)
		}
		defer in.Close()
		tmp := t.TempDir()
		dst := tmp + "/Quarterly Report 2026.pdf"
		out, err := os.Create(dst)
		if err != nil {
			t.Fatalf("create %s: %v", dst, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			t.Fatalf("copy to %s: %v", dst, err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("close %s: %v", dst, err)
		}

		root := documentRootOf(t, collectFixture(t, dst))
		if got := root.GetSymbolName(); got != "Quarterly Report 2026" {
			t.Errorf("document root SymbolName = %q, want the file basename stem %q", got, "Quarterly Report 2026")
		}
		if got := root.GetMetadata()["title_source"]; got != "filename" {
			t.Errorf("title_source = %q, want %q", got, "filename")
		}
	})

	// WHY THIS LEG EXISTS: no checked-in fixture can reach it. Weakening the
	// info_dict guard to `meta.Title != ""` compiles, keeps every other leg
	// green, and ships a document root whose SymbolName is "   " stamped
	// info_dict — which makes heading_path render a three-space path, because
	// the recipe reader skips an ancestor only on the EMPTY string.
	t.Run("whitespace_info_title_falls_through", func(t *testing.T) {
		t.Parallel()
		chunks := []pdf.Chunk{{Kind: pdf.BlockHeading, Text: "Real Heading", HeadingLevel: 1, PageRange: [2]int{0, 0}}}
		title, src := deriveDocumentTitle(pdf.Metadata{Title: "   "}, "/docs/Quarterly Report 2026.pdf", chunks)
		if title != "Real Heading" || src != titleSourceFirstHeading {
			t.Errorf("whitespace-only Info Title -> (%q, %q), want (%q, %q) - it must fall through, not render as blank",
				title, src, "Real Heading", titleSourceFirstHeading)
		}
	})

	// WHY THIS LEG EXISTS: no checked-in fixture can reach it either. Replacing
	// the direct-child loop with a recursive walk into Children also compiles
	// and also keeps every other leg green — the tagged path puts its heading
	// at the top level and the untagged path has none — but on a real untagged
	// document, whose single top-level chunk is a text-less wrapper holding
	// every classified block, it would promote a mid-document heading to the
	// document's title.
	t.Run("nested_heading_is_not_the_document_title", func(t *testing.T) {
		t.Parallel()
		chunks := []pdf.Chunk{{
			PageRange: [2]int{0, 0},
			Children: []pdf.Chunk{
				{Kind: pdf.BlockHeading, Text: "Deep Heading", HeadingLevel: 1, PageRange: [2]int{0, 0}},
			},
		}}
		// Not decoration: if the classifier ever mapped a zero-Kind chunk to
		// "section", this fixture would stop exercising the nested case and the
		// leg would pass for the wrong reason.
		if got := nodeTypeForChunk(chunks[0]); got == "section" {
			t.Fatalf("the wrapper chunk classified as %q; this leg needs a NON-section top level or it proves nothing", got)
		}
		title, src := deriveDocumentTitle(pdf.Metadata{}, "/docs/Quarterly Report 2026.pdf", chunks)
		if title != "Quarterly Report 2026" || src != titleSourceFilename {
			t.Errorf("heading nested under a non-section top-level chunk -> (%q, %q), want (%q, %q) - only a DIRECT child of the document root is a top-level heading",
				title, src, "Quarterly Report 2026", titleSourceFilename)
		}
	})

	t.Run("vocabulary_and_placement", func(t *testing.T) {
		t.Parallel()
		for _, fixture := range []string{rfcFixture, taggedFixture, headlessFixture} {
			nodes := collectFixture(t, fixture)
			root := documentRootOf(t, nodes)

			stamp, ok := root.GetMetadata()["title_source"]
			if !ok {
				t.Errorf("%s: the document root carries no title_source; the key is unconditional on every version-3 root", fixture)
			} else if !titleSourceVocabulary[stamp] {
				t.Errorf("%s: title_source = %q, which is outside the declared vocabulary %v", fixture, stamp, keysOf(titleSourceVocabulary))
			}
			if root.GetSymbolName() == "" {
				t.Errorf("%s: document root SymbolName is empty; every leg of the chain returns a non-empty title", fixture)
			}

			// PLACEMENT: the stamp describes the DOCUMENT's title and belongs
			// to the root alone. A per-chunk copy would make it look like a
			// property of every node.
			for _, n := range nodes {
				if n.GetId() == root.GetId() {
					continue
				}
				if v, carried := n.GetMetadata()["title_source"]; carried {
					t.Errorf("%s: non-document node %s (type %q) carries title_source=%q; the stamp belongs to the document root and nothing else",
						fixture, n.GetId(), n.GetType(), v)
				}
			}
		}
	})
}

// keysOf renders a set's members for a failure message, so a value outside the
// vocabulary is reported beside the vocabulary it fell outside of.
func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
