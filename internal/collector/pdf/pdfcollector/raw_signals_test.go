// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
)

// raw_signals_test.go — observations of what the pdf collector EMITS
// once the classifier stamps its own inputs onto every block: the nine
// always-on raw signals, their wire cost, and the Content convention
// that says where a raw node's searchable text lives.
//
// Every test here drives the production entry point PDFCollector.Collect
// rather than calling emit() on hand-built chunks: the subject is what a
// real collect produces, and a hand-built chunk would agree with the
// emitter by construction.

const (
	rfcFixture    = "../testdata/corpus/rfc-7234-caching/source.pdf"
	taggedFixture = "../testdata/corpus/tagged-libreoffice-sample/source.pdf"

	// signalBytesPerNodeCeiling is the wire budget for the raw-signal
	// metadata, in bytes per block-derived node. The plan's key set
	// measures under it; the three candidates it dropped (bbox,
	// primary_font, indent_columns) push it over.
	signalBytesPerNodeCeiling = 240.0
)

// signalKeys is EVERY metadata key the raw-signal work stamps: the nine
// always-on layout signals plus the three chrome keys that ride only
// the blocks a repeated fingerprint applies to. Nothing this work adds
// to a node's metadata sits outside this list, which is what makes the
// stripped control below a true measurement of its cost.
func signalKeys() []string {
	keys := make([]string, 0, len(classify.RawSignalKeys)+3)
	keys = append(keys, classify.RawSignalKeys...)
	keys = append(keys, chunk.ChromeSignalKeys...)
	return keys
}

// collectFixture runs the production collector over a repo-relative
// fixture path and returns the emitted nodes.
func collectFixture(t *testing.T, relPath string) []*knowledgev1.Node {
	t.Helper()
	abs, err := filepath.Abs(relPath)
	if err != nil {
		t.Fatalf("resolve %s: %v", relPath, err)
	}
	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), abs, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect(%s): %v", relPath, err)
	}
	return res.Nodes
}

// isBlockDerived reports whether n corresponds to a layout block. The
// test identifies the population WITHOUT reference to the signal keys —
// a node is block-derived when it carries a chunk_kind — so the
// statement stays true on a tree that stamps nothing. The document root
// and the synthetic section root correspond to no block and carry none.
func isBlockDerived(n *knowledgev1.Node) bool {
	return n.GetMetadata()["chunk_kind"] != ""
}

// TestRawSignals_EveryChunkNodeCarriesTheSignalSet observes the nine
// always-on signals on every block-derived node of a real collect:
// present, numeric, internally consistent, and calibrated against ONE
// document-wide body size rather than a per-page one.
func TestRawSignals_EveryChunkNodeCarriesTheSignalSet(t *testing.T) {
	t.Parallel()
	nodes := collectFixture(t, rfcFixture)

	bodySizes := map[string]int{}
	checked, exempt := 0, 0
	for _, n := range nodes {
		if !isBlockDerived(n) {
			exempt++
			continue
		}
		checked++
		md := n.GetMetadata()
		for _, k := range classify.RawSignalKeys {
			v, ok := md[k]
			if !ok {
				t.Errorf("node %s (%s) missing raw signal %q", n.GetId(), n.GetType(), k)
				continue
			}
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				t.Errorf("node %s: raw signal %s = %q does not parse as a number: %v", n.GetId(), k, v, err)
			}
		}
		bodySizes[md[classify.SignalBodyFontSizePt]]++

		size, _ := strconv.ParseFloat(md[classify.SignalFontSizePt], 64)
		body, _ := strconv.ParseFloat(md[classify.SignalBodyFontSizePt], 64)
		gotRatio, _ := strconv.ParseFloat(md[classify.SignalFontRatioToBody], 64)
		wantRatio := 0.0
		if body > 0 {
			wantRatio = size / body
		}
		if gotRatio != wantRatio {
			t.Errorf("node %s: font_ratio_to_body = %v, want font_size_pt/body_font_size_pt = %v", n.GetId(), gotRatio, wantRatio)
		}
	}

	if checked == 0 {
		t.Fatal("no block-derived nodes were checked — the fixture emitted nothing to observe")
	}
	// Only the document root and the single synthetic section root may
	// be exempt. A third exemption means a real block-derived node
	// slipped out of the population.
	if exempt > 2 {
		t.Errorf("%d nodes carry no chunk_kind, want at most 2 (the document root and the synthetic section root)", exempt)
	}
	// body_font_size_pt is a DOCUMENT property: a stamp fed a per-page
	// calibration would show one value per distinct page body size.
	if len(bodySizes) != 1 {
		t.Errorf("body_font_size_pt took %d distinct values across the document %v, want exactly 1 - the stamp was fed a per-page body size", len(bodySizes), bodySizes)
	}
	t.Logf("checked=%d exempt=%d body_font_size_pt=%v", checked, exempt, bodySizes)
}

// TestRawSignals_SignalPayloadPerNodeUnderCeiling measures what the raw
// signals cost on the wire, against a control drawn from the SAME run,
// the same nodes and the same field: the identical node set with
// exactly the signal keys deleted.
//
// The population is the block-derived nodes only. The document root
// carries Info-dict metadata (path, title, producer) that is not a
// signal, and including it would let those bytes register as signal
// bytes and leave the control unable to discriminate.
func TestRawSignals_SignalPayloadPerNodeUnderCeiling(t *testing.T) {
	t.Parallel()
	nodes := collectFixture(t, rfcFixture)
	keys := signalKeys()

	var full, stripped int
	var measured int
	for _, n := range nodes {
		if !isBlockDerived(n) {
			continue
		}
		measured++
		full += proto.Size(n)

		bare, ok := proto.Clone(n).(*knowledgev1.Node)
		if !ok {
			t.Fatalf("proto.Clone returned %T, want *knowledgev1.Node", bare)
		}
		for _, k := range keys {
			delete(bare.GetMetadata(), k)
		}
		stripped += proto.Size(bare)
	}

	if measured == 0 {
		t.Fatal("no block-derived nodes were measured — nothing to weigh")
	}
	// DISCRIMINATING CONTROL. On a tree that stamps nothing the two
	// totals are equal by construction, so an equality is a failure
	// naming itself rather than a small per-node figure passing
	// vacuously.
	if full == stripped {
		t.Fatalf("control did not discriminate: full=%d stripped=%d - no signal bytes were measured", full, stripped)
	}

	perNode := float64(full-stripped) / float64(measured)
	t.Logf("measuredNodes=%d full=%d stripped=%d signalBytesPerNode=%.1f ceiling=%.0f",
		measured, full, stripped, perNode, signalBytesPerNodeCeiling)
	if perNode > signalBytesPerNodeCeiling {
		t.Errorf("raw signals cost %.1f wire bytes per node, ceiling %.0f - the emitted key set is over budget",
			perNode, signalBytesPerNodeCeiling)
	}
}

// TestContentConvention_PdfSectionAndDocumentRootCarryTheirTextInContent
// observes that every pdf raw node's searchable text is emitted in
// Content — the leaves that already were, plus the two kinds that used
// to hold their text elsewhere.
//
// Two fixtures, and the reason is measured rather than stylistic:
// tagged-libreoffice-sample is the only in-repo pdf fixture that emits
// a section at all, and rfc-7234-caching is the only one whose Info
// dict carries a title and an author, so it is the only one whose
// document root has a blurb to carry.
func TestContentConvention_PdfSectionAndDocumentRootCarryTheirTextInContent(t *testing.T) {
	t.Parallel()

	t.Run("tagged_fixture_sections_and_leaves", func(t *testing.T) {
		t.Parallel()
		nodes := collectFixture(t, taggedFixture)
		sections, leaves, exempt := 0, 0, 0
		for _, n := range nodes {
			if n.GetType() == "document" {
				continue
			}
			// The synthetic section root corresponds to no layout block
			// and carries no text of its own.
			if !isBlockDerived(n) {
				exempt++
				continue
			}
			if n.GetType() == "section" {
				sections++
				if n.GetContent() != n.GetSymbolName() {
					t.Errorf("section %s: Content = %q, want its heading %q", n.GetId(), n.GetContent(), n.GetSymbolName())
				}
				// A section whose heading is empty has no searchable
				// text to carry, so it is exempt from the non-empty
				// check while still having to satisfy Content == heading.
				if n.GetSymbolName() != "" && n.GetContent() == "" {
					t.Errorf("section %s: Content is empty though the heading is %q", n.GetId(), n.GetSymbolName())
				}
				continue
			}
			leaves++
			if n.GetContent() == "" {
				t.Errorf("leaf node %s (%s) carries no Content", n.GetId(), n.GetType())
			}
		}
		if sections == 0 || leaves == 0 {
			t.Fatalf("fixture yielded sections=%d leaves=%d exempt=%d, want at least one of each", sections, leaves, exempt)
		}
		t.Logf("fixture yielded sections=%d leaves=%d exempt=%d", sections, leaves, exempt)
	})

	t.Run("schema_version_stamp", func(t *testing.T) {
		t.Parallel()
		// THE LITERAL IS THE POINT. collectorSchemaVersion documents
		// itself as bumped in the same change as any alteration to what
		// this collector emits, and nothing else in the tree pins the
		// pdf value: the corpus fixtures carry synthetic versions, and
		// the sibling collector's equivalent test compares against its
		// own constant and so cannot see a wrong value. Asserting the
		// emitted stamp against a LITERAL is what makes the bump
		// obligatory — a shape change moves the constant AND this
		// number together, and a shape change that moves neither fails
		// here rather than shipping a graph whose consumers cannot tell
		// which shape they are reading.
		nodes := collectFixture(t, rfcFixture)
		roots, stamp := 0, ""
		for _, n := range nodes {
			if n.GetType() != "document" {
				continue
			}
			roots++
			stamp = n.GetMetadata()["collector_schema_version"]
			if stamp != "3" {
				t.Errorf("document root collector_schema_version = %q, want \"3\" - the emitted shape and the stamp have diverged", stamp)
			}
		}
		if roots != 1 {
			t.Fatalf("emitted %d document roots, want exactly 1", roots)
		}
		// Log the OBSERVED value, not the wanted one: a log line that
		// prints the expectation reads as confirmation on a failing run.
		t.Logf("document root carries collector_schema_version=%q", stamp)
	})

	t.Run("document_root_blurb", func(t *testing.T) {
		t.Parallel()
		// EXTERNAL EXPECTATION: the author is read from the PDF's own
		// Info dict, not from the emitter, so the subject does not
		// supply its own answer key.
		abs, err := filepath.Abs(rfcFixture)
		if err != nil {
			t.Fatalf("resolve fixture: %v", err)
		}
		doc, err := pdf.OpenFile(abs)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		wantAuthor := doc.Metadata().Author
		doc.Close()
		if wantAuthor == "" {
			t.Fatal("the rfc-7234-caching Info dict carries no author; this fixture cannot witness the blurb")
		}

		nodes := collectFixture(t, rfcFixture)
		roots := 0
		for _, n := range nodes {
			if n.GetType() != "document" {
				continue
			}
			roots++
			if !strings.Contains(n.GetContent(), wantAuthor) {
				t.Errorf("document root Content %q does not carry the blurb author %q", n.GetContent(), wantAuthor)
			}
			if n.GetDescription() != "" {
				t.Errorf("document root Description = %q, want empty - the blurb moved to Content", n.GetDescription())
			}
		}
		if roots != 1 {
			t.Errorf("emitted %d document roots, want exactly 1", roots)
		}
		t.Logf("document root carries the Info-dict author %q in Content", wantAuthor)
	})
}
