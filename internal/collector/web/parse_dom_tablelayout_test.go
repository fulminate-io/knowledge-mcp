// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// collectedRecords is a flattened view of one parsed page: every section
// reachable from the top-level slice, and every content record beneath any
// of them, partitioned by kind. Nested sections are followed, so a record
// under an H3 inside an H2 is counted exactly once.
type collectedRecords struct {
	sections   []*sectionRecord
	paragraphs []paragraphRecord
	tables     []tableRecord
	lists      []listRecord
	listItems  []listItemRecord
	codeBlocks []codeBlockRecord
	links      []linkRecord
	images     []imageRecord
	quotes     []quoteRecord
}

// collectRecords walks rec's section tree and returns every record it holds.
func collectRecords(rec *pageRecord) *collectedRecords {
	out := &collectedRecords{}
	var walkSection func(*sectionRecord)
	walkSection = func(s *sectionRecord) {
		if s == nil {
			return
		}
		out.sections = append(out.sections, s)
		for _, child := range s.Children {
			switch r := child.(type) {
			case nestedSectionRecord:
				walkSection(r.Section)
			case paragraphRecord:
				out.paragraphs = append(out.paragraphs, r)
			case tableRecord:
				out.tables = append(out.tables, r)
			case listRecord:
				out.lists = append(out.lists, r)
				out.listItems = append(out.listItems, r.Items...)
			case codeBlockRecord:
				out.codeBlocks = append(out.codeBlocks, r)
			case linkRecord:
				out.links = append(out.links, r)
			case imageRecord:
				out.images = append(out.images, r)
			case quoteRecord:
				out.quotes = append(out.quotes, r)
			}
		}
	}
	for _, s := range rec.TopSections {
		walkSection(s)
	}
	return out
}

// parseFixture loads a testdata fixture and runs it through parsePage,
// failing the test on any parse error. Every fixture is parsed against the
// same synthetic base URL so link classification is comparable across them.
func parseFixture(t *testing.T, name string) *pageRecord {
	t.Helper()
	raw := loadFixture(t, name)
	rec, err := parsePage(fakeFetched(tableLayoutURL, string(raw)), fakeCleaned("Table Layout Page", string(raw)))
	if err != nil {
		t.Fatalf("parsePage(%s): %v", name, err)
	}
	if rec == nil {
		t.Fatalf("parsePage(%s): nil pageRecord", name)
	}
	return rec
}

// anyParagraphContains reports whether some paragraph's Text contains sub.
func anyParagraphContains(paras []paragraphRecord, sub string) bool {
	for _, p := range paras {
		if strings.Contains(p.Text, sub) {
			return true
		}
	}
	return false
}

const tableLayoutURL = "https://example.test/definitions/78.html"

// TestParsePage_TableLayoutPage_EmitsProse pins the defect this fixture was
// authored for: a page whose entire content sits inside a layout <table>
// emitted no prose at all, because atom.Table is a terminal handler and the
// walker never descended into the wrapper's subtree.
//
// The taxonomy leg is the ticket's re-scoped CAPABILITY claim, demonstrated
// rather than asserted. It lives in this test rather than standing alone
// because its red-first property depends entirely on the taxonomy table
// sitting INSIDE table#MainPane — outside the wrapper handleTable already
// emits it, so a standalone assertion would be green from birth.
func TestParsePage_TableLayoutPage_EmitsProse(t *testing.T) {
	t.Parallel()
	got := collectRecords(parseFixture(t, "cwe_table_layout.html"))

	if len(got.paragraphs) == 0 {
		t.Fatalf("zero paragraphRecords recovered from a page whose content lives inside a layout table")
	}

	const descriptionProse = "assembles a command string from values a caller supplies"
	if !anyParagraphContains(got.paragraphs, descriptionProse) {
		t.Errorf("no paragraph carries the Description prose %q; got %d paragraphs", descriptionProse, len(got.paragraphs))
	}

	const codeExampleProse = "shell_exec($listing);"
	if !anyParagraphContains(got.paragraphs, codeExampleProse) {
		t.Errorf("no paragraph carries the code-example prose %q; got %d paragraphs", codeExampleProse, len(got.paragraphs))
	}

	assertTaxonomyTable(t, got.tables)
}

// assertTaxonomyTable is the taxonomy leg: a Nature-headed relationship
// table must survive as a tableRecord whose rows keep the relationship type
// and its target together on one row.
func assertTaxonomyTable(t *testing.T, tables []tableRecord) {
	t.Helper()
	wantHeaders := []string{"Nature", "Type", "ID", "Name"}
	var taxonomy *tableRecord
	for i := range tables {
		if slicesEqual(tables[i].Headers, wantHeaders) {
			taxonomy = &tables[i]
			break
		}
	}
	if taxonomy == nil {
		t.Fatalf("no tableRecord with Headers %v; saw %d tables with headers %v",
			wantHeaders, len(tables), allHeaders(tables))
	}

	fourCell := 0
	childOf := false
	for _, row := range taxonomy.Rows {
		if len(row) != 4 {
			continue
		}
		fourCell++
		if row[0] == "ChildOf" && row[2] != "" && row[3] != "" {
			childOf = true
		}
	}
	if fourCell < 2 {
		t.Errorf("taxonomy table has %d four-cell rows, want at least 2; rows=%v", fourCell, taxonomy.Rows)
	}
	if !childOf {
		t.Errorf("no taxonomy row pairs ChildOf with a non-empty ID and Name; rows=%v", taxonomy.Rows)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func allHeaders(tables []tableRecord) [][]string {
	out := make([][]string, 0, len(tables))
	for _, tbl := range tables {
		out = append(out, tbl.Headers)
	}
	return out
}

// TestParsePage_TableLayoutPage_NoCollapse kills the failure mode the prose
// assertions cannot see: an inverted partition that accumulates the whole
// document into ONE run and emits it as a single giant paragraph. That state
// satisfies every prose assertion in EmitsProse while destroying every
// section, every nested data table and every distinct prose block.
//
// Each leg kills it independently.
func TestParsePage_TableLayoutPage_NoCollapse(t *testing.T) {
	t.Parallel()
	got := collectRecords(parseFixture(t, "cwe_table_layout.html"))

	const wantParagraphs = 12
	if len(got.paragraphs) < wantParagraphs {
		t.Errorf("got %d paragraphRecords, want at least %d — the fixture carries that many distinct prose blocks, so a whole-document collapse lands here",
			len(got.paragraphs), wantParagraphs)
	}

	const maxParagraphLen = 2000
	for i, p := range got.paragraphs {
		if len(p.Text) > maxParagraphLen {
			t.Errorf("paragraph %d is %d chars, over the %d-char ceiling — that is a whole-document collapse, not a prose block: %.200q",
				i, len(p.Text), maxParagraphLen, p.Text)
		}
	}

	headed := 0
	for _, s := range got.sections {
		if strings.TrimSpace(s.Heading) != "" {
			headed++
		}
	}
	if headed == 0 {
		t.Errorf("no sectionRecord carries a non-empty Heading — the fixture's in-wrapper <h2> was absorbed into a run instead of pushing a section; %d sections seen",
			len(got.sections))
	}
}

// recordKindCounts returns the per-kind record census for one parsed page.
func recordKindCounts(got *collectedRecords) map[string]int {
	return map[string]int{
		"section":    len(got.sections),
		"paragraph":  len(got.paragraphs),
		"table":      len(got.tables),
		"list":       len(got.lists),
		"list_item":  len(got.listItems),
		"code_block": len(got.codeBlocks),
		"link":       len(got.links),
		"image":      len(got.images),
		"blockquote": len(got.quotes),
	}
}

// TestParsePage_ControlFixtures_NoContentLoss is the ADDITIVE-PROPERTY gate:
// the run model may add records on any fixture, but it must never remove one.
//
// THE FLOORS ARE TREE-DERIVED. Each was measured by running the UNCHANGED
// parsePage over the fixture before any source change in this changeset, so
// they are observations rather than aspirations. Floors rather than equality
// because the run model legitimately ADDS paragraphs — equality would be red
// against correct work, while a floor is red only against loss.
//
// The link floors are the ones that were hardest to hold: under the
// superseded suppression rule go101_sample's link floor of 3 measured 0, and
// this gate went red against an otherwise-correct implementation. They hold
// only because flushInlineRun emits a record for every anchor in a run.
//
// WHAT THIS GATE CANNOT SEE. Neither hohpe_sample.html nor go101_sample.html
// contains a single <table> element, so it is blind to table classification
// entirely; that is covered by TestIsLayoutTable_Classification against
// table_shapes.html and by the data-table control on the CWE fixture.
func TestParsePage_ControlFixtures_NoContentLoss(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name string
		// floors are the pre-change measured counts, per record kind.
		floors map[string]int
		// verbatim texts that a paragraph carried before the change, or —
		// for divprose_blocks.html, which emitted ZERO paragraphs before the
		// change — prose the fixture's markup mandates. Noted honestly
		// because the pre-change set for that fixture is empty.
		verbatim []string
		// classValues are author-marked class attributes that must survive
		// onto a paragraph's Attrs.Class. This is the v1.1 attribute
		// contract. NOTE: no checked-in fixture carries a pattern-* class,
		// so that exact spelling cannot be exercised here; these are the
		// author-marked classes the fixtures actually supply, and they
		// exercise the same mechanism.
		classValues []string
	}{
		{
			name: "hohpe_sample.html",
			floors: map[string]int{
				"section": 6, "paragraph": 6, "table": 0, "list": 2,
				"list_item": 3, "code_block": 0, "link": 0, "image": 1, "blockquote": 0,
			},
			verbatim: []string{
				"When two applications need to exchange information, they connect via a Message Channel.",
				"A Message Channel is a logical pathway",
				"How can two applications communicate reliably without requiring that both be available at the same time?",
				"Use a Message Channel.",
			},
			classValues: []string{"byline"},
		},
		{
			name: "go101_sample.html",
			floors: map[string]int{
				"section": 4, "paragraph": 4, "table": 0, "list": 1,
				"list_item": 3, "code_block": 2, "link": 3, "image": 0, "blockquote": 0,
			},
			verbatim: []string{
				"In Go, channels are a typed conduit through which you can send and receive values",
				"Closing a channel signals to receivers that no more values will be sent:",
				"The `select` statement multiplexes over multiple channel operations",
			},
			classValues: []string{"topnav"},
		},
		{
			name: "divprose_blocks.html",
			floors: map[string]int{
				"section": 1, "paragraph": 0, "table": 0, "list": 1,
				"list_item": 2, "code_block": 0, "link": 2, "image": 0, "blockquote": 0,
			},
			verbatim: []string{
				"A channel is a typed conduit through which one goroutine hands a value",
				"Select chooses among ready cases uniformly at random",
			},
			classValues: []string{"tmd-usual"},
		},
	}

	for _, fx := range fixtures {
		got := collectRecords(parseFixture(t, fx.name))
		counts := recordKindCounts(got)

		for kind, floor := range fx.floors {
			if counts[kind] < floor {
				t.Errorf("%s: record kind %q fell from its pre-change floor of %d to %d — this change must only ADD records",
					fx.name, kind, floor, counts[kind])
			}
		}

		for _, want := range fx.verbatim {
			if !anyParagraphContains(got.paragraphs, want) {
				t.Errorf("%s: no paragraph carries the pinned text %q; got %d paragraphs",
					fx.name, want, len(got.paragraphs))
			}
		}

		for _, want := range fx.classValues {
			if !anyParagraphHasClass(got.paragraphs, want) {
				t.Errorf("%s: no paragraph carries Attrs.Class %q — the author-marked class attribute was dropped; classes seen: %v",
					fx.name, want, paragraphClasses(got.paragraphs))
			}
		}
	}
}

// anyParagraphHasClass reports whether some paragraph's Attrs.Class contains
// the given class token.
func anyParagraphHasClass(paras []paragraphRecord, class string) bool {
	for _, p := range paras {
		for field := range strings.FieldsSeq(p.Attrs.Class) {
			if field == class {
				return true
			}
		}
	}
	return false
}

func paragraphClasses(paras []paragraphRecord) []string {
	out := make([]string, 0, len(paras))
	for _, p := range paras {
		out = append(out, p.Attrs.Class)
	}
	return out
}

// TestParsePage_DivWrappedProse is the ONLY gate on the ticket's go101
// requirement. go101_sample.html is semantic article/h1/p markup and
// structurally cannot exercise div-wrapped prose; divprose_blocks.html
// reproduces the live shape, where article prose sits in sibling
// div class="tmd-usual" blocks with no <p> anywhere.
func TestParsePage_DivWrappedProse(t *testing.T) {
	t.Parallel()
	got := collectRecords(parseFixture(t, "divprose_blocks.html"))

	// The fixture carries 13 tmd-usual blocks; the floor is the 10 the
	// criterion names, so adding or removing a block does not silently
	// change what this gate means.
	const wantParagraphs = 10
	if len(got.paragraphs) < wantParagraphs {
		t.Errorf("got %d paragraphRecords, want at least %d — one per div class=\"tmd-usual\" prose block",
			len(got.paragraphs), wantParagraphs)
	}

	// One paragraph PER BLOCK, not one giant run: every emitted paragraph
	// must carry the block's own class.
	for i, p := range got.paragraphs {
		if p.Attrs.Class != "tmd-usual" {
			t.Errorf("paragraph %d carries class %q, want \"tmd-usual\" — a run took its attrs from somewhere other than its own block: %.80q",
				i, p.Attrs.Class, p.Text)
		}
	}

	// The inline-anchor blocks stay intact: their prose survives, anchor text
	// included, AND each anchor still emits its own link record.
	//
	// Compared against the FLATTENED paragraph text, because collapseProseLines
	// preserves every newline in the collected text — including the ones the
	// fixture's HTML source carries as hard-wrapping, not just the ones <br>
	// writes. That is a direct consequence of the specified per-line collapse
	// and it is pinned explicitly below rather than left as an accident.
	for _, want := range []string{
		"The buffered form relaxes that by admitting a fixed number of queued values.",
		"The select statement is where that behaviour pays off.",
	} {
		if !anyParagraphContains(flattenParagraphs(got.paragraphs), want) {
			t.Errorf("an inline-anchor block lost its prose: no paragraph carries %q", want)
		}
	}
	if len(got.links) != 2 {
		t.Errorf("got %d link records, want 2 — one per inline anchor in the tmd-usual blocks", len(got.links))
	}

	// PINNED: a paragraph built from an inline run keeps the source's own line
	// wrapping as newlines, whereas one built by handleParagraph from a <p>
	// does not (collectProseText flattens). Asserted so the asymmetry is
	// visible and any change to it is caught rather than discovered later.
	multiline := 0
	for _, p := range got.paragraphs {
		if strings.Contains(p.Text, "\n") {
			multiline++
		}
	}
	if multiline == 0 {
		t.Errorf("no run-built paragraph carries a newline — collapseProseLines is expected to preserve the fixture's source line wrapping; if that changed deliberately, update this assertion and the finding it records")
	}
}

// flattenParagraphs returns copies of paras whose Text has been collapsed to
// a single line, for assertions about prose content rather than line shape.
func flattenParagraphs(paras []paragraphRecord) []paragraphRecord {
	out := make([]paragraphRecord, 0, len(paras))
	for _, p := range paras {
		p.Text = strings.Join(strings.Fields(p.Text), " ")
		out = append(out, p)
	}
	return out
}
