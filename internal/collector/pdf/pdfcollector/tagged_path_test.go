// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
)

// tagged_path_test.go — the structure-tree path, observed through the
// production emitter.
//
// A tagged PDF tells you what each region IS: the author declared this
// one a heading and that one a table. Nothing in the chunking path used
// to call the reader that recovers those declarations, so a genuinely
// tagged document collected to zero roles and zero tables and every
// region was re-guessed from font size. Tables in particular are not
// recoverable any other way — there is no geometric heuristic for one.
//
// ORDER AND ROLES ARE ASSERTED TOGETHER, deliberately. The two failure
// modes this covers look nothing alike and both produce plausible
// output on their own: a page that never reached the structure tree
// emits roleless paragraphs in the right order, and a page that reached
// it through a merge sorted the wrong way emits every role correctly in
// exactly reverse reading order.

// writeTaggedFixture synthesizes a one-page tagged PDF whose structure
// tree labels a heading, a paragraph and a table laid out top to
// bottom, with one UNTAGGED paragraph below them. The untagged run is
// what makes this a hybrid page: it reaches the emitter only through
// the residue clustering that the structure-tree read merges back in.
func writeTaggedFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "tagged_roles_and_table.pdf")

	body := "/H1 << /MCID 1 >> BDC\n" +
		"BT /F1 18 Tf 100 750 Td (Structure Tree Heading) Tj ET\n" +
		"EMC\n" +
		"/P << /MCID 2 >> BDC\n" +
		"BT /F1 12 Tf 100 720 Td (A tagged paragraph.) Tj ET\n" +
		"EMC\n" +
		"/Table << /MCID 3 >> BDC\n" +
		"BT /F1 12 Tf 100 690 Td (Column one Column two) Tj ET\n" +
		"EMC\n" +
		"BT /F1 12 Tf 100 660 Td (An untagged trailing paragraph.) Tj ET\n"

	spec := fixturelib.TaggedPageSpec{
		Fonts: fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"}),
		Body:  body,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{
				{Type: "H1", MCIDs: []int{1}},
				{Type: "P", MCIDs: []int{2}},
				{Type: "Table", MCIDs: []int{3}},
			},
		},
	}
	if err := fixturelib.WriteTaggedPDF(dst, spec); err != nil {
		t.Fatalf("WriteTaggedPDF: %v", err)
	}
	return dst
}

// typeRole is one emitted node reduced to the two properties this
// criterion is about.
type typeRole struct {
	typ  string
	role string
}

func TestTaggedPath_EmitsRolesTablesAndReadingOrder(t *testing.T) {
	t.Parallel()
	path := writeTaggedFixture(t)

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), path, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := make([]typeRole, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		if n.GetType() == "document" {
			continue
		}
		got = append(got, typeRole{n.GetType(), n.GetMetadata()["struct_role"]})
	}

	want := []typeRole{
		{"section", "H1"},
		{"paragraph", "P"},
		{"table", "Table"},
		{"paragraph", ""},
	}

	if len(got) != len(want) {
		t.Fatalf("emitted %d chunk nodes %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v (full order %v)", i, got[i], want[i], got)
		}
	}
}

// TestTaggedPath_DehyphenatesAcrossRenderedLineBreaks drives the whole
// chain on a tagged document whose paragraph breaks a word across two
// rendered lines.
//
// This is the completeness hole the tagged path opened. The ticket asks
// both for the structure-tree path to be reachable AND for words broken
// at a line end to be rejoined, and until the walker reconstructed
// rendered lines the two could not both hold: a tagged element arrived
// as ONE layout.Line, so there was no boundary for the hyphen heuristic
// to act on. Measured before the fix, this fixture emitted
// "sequen-tially"; the identical text collected through the UNTAGGED
// path has always emitted "sequentially".
func TestTaggedPath_DehyphenatesAcrossRenderedLineBreaks(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "tagged_hyphen.pdf")

	// One <P> element, two rendered lines, broken at a hyphenated word
	// with a lowercase continuation.
	body := "/P << /MCID 1 >> BDC\n" +
		"BT /F1 12 Tf 100 700 Td (the broker writes records sequen-) Tj ET\n" +
		"BT /F1 12 Tf 100 686 Td (tially to the log) Tj ET\n" +
		"EMC\n"
	spec := fixturelib.TaggedPageSpec{
		Fonts: fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"}),
		Body:  body,
		StructTree: fixturelib.StructElemSpec{
			Type:     "Document",
			Children: []fixturelib.StructElemSpec{{Type: "P", MCIDs: []int{1}}},
		},
	}
	if err := fixturelib.WriteTaggedPDF(dst, spec); err != nil {
		t.Fatalf("WriteTaggedPDF: %v", err)
	}

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), dst, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var para string
	for _, n := range res.Nodes {
		if n.GetType() == "paragraph" {
			para = n.GetContent()
		}
	}
	if para == "" {
		t.Fatal("the tagged fixture emitted no paragraph node; there is nothing to observe")
	}
	t.Logf("tagged paragraph content = %q", para)

	if !strings.Contains(para, "sequentially") {
		t.Errorf("tagged paragraph %q does not contain the rejoined word %q", para, "sequentially")
	}
	if strings.Contains(para, "sequen-") {
		t.Errorf("tagged paragraph %q still carries the line-break hyphen mid-word", para)
	}
	if strings.Contains(para, "sequen tially") {
		t.Errorf("tagged paragraph %q inserted a space where the hyphen was removed", para)
	}
}
