// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
)

// help_recipes_fixture_values_test.go is the VALUE half of the fixture fidelity
// gate. Its sibling in help_recipes_fixture_test.go holds the fixture graphs to
// the SHAPE their collectors emit — which fields are populated, which are empty,
// what an edge may target. This file holds them to the VALUES their collectors
// declare.
//
// WHY THE SPLIT IS NOT COSMETIC. A shape assertion is satisfied by any non-empty
// string, and that is exactly how the drift got in: the fixture stamped
// collector_schema_version "1" while both collectors' constants read 3, and the
// gate that asserted the key was present said nothing for as long as it was
// wrong. Presence is not fidelity. A fixture field whose value mirrors something
// a collector DECLARES is a second declaration of that thing, free to drift from
// the first without the compiler noticing — which is the same argument the
// title_source const block makes about a bare literal at a stamping site, and
// the same one an admitted corpus check enforces on that key across the module.
//
// SO THE COLLECTORS EXPORT WHAT THEY DECLARE and this file cites them:
// pdfcollector.CollectorSchemaVersion, web.CollectorSchemaVersion, the three
// title-source words in the const block beside pdfcollector.TitleSourceInfoDict,
// and pdfcollector.BuildDocumentBlurb. The alternative — a per-key corpus check on each literal — catches the spelling of
// one key and can say nothing about the blurb, which has no constant to cite
// because it is a composition. Composing it through the exported builder is what
// makes the third element assertable at all.

// fixtureValueCase is one fixture field compared against the value a collector
// package declares, carrying enough to name all three in a failure: where the
// value sits, what it is, and which exported declaration supplied the want.
type fixtureValueCase struct {
	node    string
	field   string
	got     string
	want    string
	declSrc string
}

// pdfMetaForBlurb rebuilds the Info-dict record a document root's blurb was
// composed from, out of the root's OWN metadata keys. emitDocumentNode stamps
// title, author and subject on a non-empty guard each and composes the blurb
// from the same three plus the derived title, so reading them back and
// re-composing is a round trip through the collector's own function rather than
// a restatement of its output.
func pdfMetaForBlurb(md map[string]string) pdf.Metadata {
	return pdf.Metadata{
		Title:   md["title"],
		Author:  md["author"],
		Subject: md["subject"],
	}
}

// fixtureDeclaredValueCases derives every comparison this gate makes from the
// two fixture graphs, so a node added to either family is covered without a
// second table to keep in step.
func fixtureDeclaredValueCases() []fixtureValueCase {
	var cases []fixtureValueCase

	pdfNodes, _ := pdfFixtureGraph()
	for _, n := range pdfNodes {
		if n.Type != "document" {
			continue
		}
		cases = append(cases,
			fixtureValueCase{
				node: n.Id, field: "metadata.collector_schema_version",
				got:     n.Metadata["collector_schema_version"],
				want:    strconv.Itoa(pdfcollector.CollectorSchemaVersion),
				declSrc: "pdfcollector.CollectorSchemaVersion",
			},
			fixtureValueCase{
				node: n.Id, field: "metadata.title_source",
				got:     n.Metadata["title_source"],
				want:    pdfcollector.TitleSourceInfoDict,
				declSrc: "pdfcollector.TitleSourceInfoDict",
			},
			fixtureValueCase{
				node: n.Id, field: "Content",
				got:     n.Content,
				want:    pdfcollector.BuildDocumentBlurb(pdfMetaForBlurb(n.Metadata), n.SymbolName),
				declSrc: "pdfcollector.BuildDocumentBlurb over this root's own title/author/subject",
			},
		)
	}

	webNodes, _ := webFixtureGraph()
	for _, n := range webNodes {
		if n.Type != "page" {
			continue
		}
		cases = append(cases, fixtureValueCase{
			node: n.Id, field: "metadata.collector_schema_version",
			got:     n.Metadata["collector_schema_version"],
			want:    strconv.Itoa(web.CollectorSchemaVersion),
			declSrc: "web.CollectorSchemaVersion",
		})
	}
	return cases
}

// fixtureDeclaredValueFloor is the number of comparisons the derivation above
// must produce on the fixtures as they stand: three on the pdf document root and
// one on each of the two web pages.
//
// IT IS A FLOOR THAT IS RE-DERIVED, never a decoration — run the gate and read
// its log line. Without it a derivation that stopped finding nodes reports a
// clean pass over an empty set, which is the failure mode this whole file
// exists to close one level down.
const fixtureDeclaredValueFloor = 5

// TestFixtureGraphs_MirrorTheCollectorsDeclaredValues compares each mirrored
// fixture value against the collector's exported declaration.
//
// IT IS THE VALUE ARM OF THE SAME AGREEMENT the shape gate names on both sides.
// The production half stays where the production is — the collectors' own tests
// assert that a real emit stamps these keys — and this half asserts the fixture
// says what those declarations say, so neither side can move alone.
func TestFixtureGraphs_MirrorTheCollectorsDeclaredValues(t *testing.T) {
	cases := fixtureDeclaredValueCases()
	if len(cases) < fixtureDeclaredValueFloor {
		t.Fatalf("derived %d declared-value comparisons from the fixtures, want at least %d — the derivation found fewer nodes than the fixtures carry, so every assertion below would pass over a short set",
			len(cases), fixtureDeclaredValueFloor)
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("fixture node %s: %s = %q, want %q from %s — a fixture value that mirrors a collector declaration is a second declaration of it, and this one has drifted",
				c.node, c.field, c.got, c.want, c.declSrc)
		}
	}
	t.Logf("compared %d fixture values against the collectors' exported declarations", len(cases))
}

// TestFixtureGraphs_TitleSourceIsOneOfTheDeclaredWords is the vocabulary arm:
// the equality above pins the fixture to info_dict, and this pins it to the
// CLOSED SET the collector declares, so a fixture edited to a fourth word fails
// as an unknown value rather than only as an unequal one.
//
// THE TWO SAY DIFFERENT THINGS. Equality says the fixture models an Info-dict
// title; membership says whatever it models is a title source that exists. A
// later fixture that legitimately models a filename-derived title changes the
// first and must still satisfy the second.
func TestFixtureGraphs_TitleSourceIsOneOfTheDeclaredWords(t *testing.T) {
	declared := []string{
		pdfcollector.TitleSourceInfoDict,
		pdfcollector.TitleSourceFirstHeading,
		pdfcollector.TitleSourceFilename,
	}

	pdfNodes, _ := pdfFixtureGraph()
	checked := 0
	for _, n := range pdfNodes {
		if n.Type != "document" {
			continue
		}
		checked++
		got := n.Metadata["title_source"]
		if !slices.Contains(declared, got) {
			t.Errorf("fixture node %s stamps title_source %q, which is not one of the three words pdfcollector declares (%v) — deriveDocumentTitle returns no fourth value, so no collect produces this root",
				n.Id, got, declared)
		}
	}
	if checked == 0 {
		t.Fatal("the pdf fixture carries no document root, so the vocabulary assertion above ran over nothing")
	}
	t.Logf("checked %d document root(s) against the %d declared title_source words", checked, len(declared))
}

// TestFixtureValueCases_NameTheirDeclarationSource keeps the failure messages
// above worth reading: every derived case carries the exported name its want
// came from, so a reader of a red knows which declaration to open.
//
// IT IS A GATE ON THE INSTRUMENT, not on the fixture. A case added later with an
// empty declSrc would still compare correctly and would report a bare pair of
// quoted strings on failure, which is the shape of message that sends a reader
// looking for a second declaration they cannot find.
func TestFixtureValueCases_NameTheirDeclarationSource(t *testing.T) {
	for _, c := range fixtureDeclaredValueCases() {
		if c.declSrc == "" || c.field == "" || c.node == "" {
			t.Errorf("a derived value case is unlabeled: %s", fmt.Sprintf("node=%q field=%q declSrc=%q", c.node, c.field, c.declSrc))
		}
	}
}
