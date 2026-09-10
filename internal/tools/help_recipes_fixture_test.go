// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// helpRecipesWorkedBodyCount is the number of complete recipe bodies the help
// topic ships. It is a FLOOR THAT IS RE-DERIVED, never a decoration: run the
// sibling parse gate and read its own log line, `parsed N worked recipe bodies
// from helpRecipes`. Measured 6 on the tree before the reading-loop section and
// 11 after it.
//
// If this number moves for any reason other than adding or removing an example,
// the extractor or the help's indentation changed and THAT is the finding. Two
// bodies separated only by a blank line MERGE into one block that still parses,
// so the parse gate stays green and this count is the only detector.
const helpRecipesWorkedBodyCount = 11

// TestHelpRecipes_WorkedExamplesValidateAgainstAFixtureGraph runs every worked
// recipe body the help ships through the REAL collect dispatch, against raw
// graphs shaped like the ones the current collectors emit.
//
// WHY THIS EXISTS, over and above the parse gate. Parsing proves a body is
// grammatical. It does not prove the body names a metadata key any collector
// stamps, an edge type any collector writes, or a node type any collector
// emits — the recipe validator refuses all three BEFORE the walk, so a shipped
// example that drifted from the collectors fails at the user's first run and
// nowhere else. And a body can pass the validator and still MATCH NOTHING: a
// heading regex naming a section the collector no longer produces, a threshold
// no row clears. This gate reads `rows=` off the run's own header for exactly
// that reason — "not refused" cannot see it.
//
// BOTH FAMILIES, NO ROUTING TABLE. Every body runs against both fixtures and
// passes on either. A per-body pdf-or-web table would be a second place to keep
// in step with the help, and it would drift the first time an example moved.
//
// AND IT CATCHES THE DUPLICATE-IDENTITY REFUSAL SEPARATELY FROM THE ROW COUNT.
// Both fixtures carry two sections sharing a heading and two pages sharing a
// title, so a body that keys its emit identity on a heading-derived name is
// REFUSED on them — evalEmit stops the whole run at the first repeat. That
// refusal is reported here by name rather than only as a zero row count,
// because the rows=0 arm cannot tell it from a body whose filters matched
// nothing, and because a body refused on one family while returning rows on
// the other would otherwise pass this gate silently.
func TestHelpRecipes_WorkedExamplesValidateAgainstAFixtureGraph(t *testing.T) {
	blocks := extractRecipeBlocks()
	if len(blocks) != helpRecipesWorkedBodyCount {
		t.Fatalf("extracted %d worked recipe bodies, want exactly %d — an example left the extractor's reach (check its four-space indent) or two merged because the prose between them was dropped",
			len(blocks), helpRecipesWorkedBodyCount)
	}

	caller := helpFixtureCaller()
	for i, b := range blocks {
		t.Run(firstLine(b), func(t *testing.T) {
			pdfRows, pdfOut := runHelpFixtureExtract(t, caller, "pdf", b)
			webRows, webOut := runHelpFixtureExtract(t, caller, "web", b)
			for _, run := range []struct{ family, out string }{{"pdf", pdfOut}, {"web", webOut}} {
				if strings.Contains(run.out, identityCollisionMarker) {
					t.Errorf("help worked example %d is REFUSED on the %s fixture for a repeated emit identity — key the emit on the row's own id\n--- body\n%s\n--- %s\n%s",
						i, run.family, b, run.family, run.out)
				}
			}
			if pdfRows < 1 && webRows < 1 {
				t.Errorf("help worked example %d returned no rows on either fixture\n--- body\n%s\n--- pdf\n%s\n--- web\n%s",
					i, b, pdfOut, webOut)
			}
		})
	}
	t.Logf("validated %d worked recipe bodies against the fixture raw graphs", len(blocks))
}

// runHelpFixtureExtract drives one body through InterceptCollect in extract mode
// and returns the rows the run RETURNED, read off the run's own `extract:`
// header line — the subject's bytes rather than a wrapper's echo.
//
// It returns -1 for a refusal or an absent header, which is what keeps a
// refusal distinguishable from a clean run that matched zero rows.
func runHelpFixtureExtract(t *testing.T, caller *recipeRoutingCaller, collectType, body string) (int, string) {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"type":        collectType,
		"id":          "fixture",
		"transformer": "recipe",
		"extract":     true,
		"recipe_body": body,
	})
	require.NoError(t, err)

	deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: caller}
	handled, res := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{Name: "collect", Arguments: args})
	require.True(t, handled, "the recipe collect dispatch must handle an inline extract")
	out := resultText(res)
	if res.IsError {
		return -1, out
	}
	return helpFixtureExtractRows(out), out
}

// helpFixtureExtractRows parses the RETURNED count out of an extract header's
// `rows=<returned>/<matched>` field. -1 means there was no header to read.
func helpFixtureExtractRows(out string) int {
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "extract:") {
			continue
		}
		for f := range strings.FieldsSeq(line) {
			rest, ok := strings.CutPrefix(f, "rows=")
			if !ok {
				continue
			}
			returned, _, _ := strings.Cut(rest, "/")
			n, err := strconv.Atoi(returned)
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}

// helpFixtureCaller serves both fixture raw graphs off one name-agnostic caller,
// keyed on graph TYPE the way recipeRoutingCaller keys.
func helpFixtureCaller() *recipeRoutingCaller {
	pdfNodes, pdfEdges := pdfFixtureGraph()
	webNodes, webEdges := webFixtureGraph()
	return &recipeRoutingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphPDFRaw): pdfNodes,
			string(kgtypes.GraphWebRaw): webNodes,
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{
			string(kgtypes.GraphPDFRaw): pdfEdges,
			string(kgtypes.GraphWebRaw): webEdges,
		},
	}
}

// containsEdge mirrors both collectors' addContains: a CONTAINS edge whose
// Evidence carries the child's position as the flat JSON string map the recipe
// interpreter's reading-order index decodes.
func containsEdge(from, to string, pos int) *knowledgev1.Edge {
	return &knowledgev1.Edge{
		FromId:   from,
		ToId:     to,
		Type:     string(kgtypes.EdgeContains),
		Evidence: `{"position":"` + strconv.Itoa(pos) + `"}`,
	}
}

// pdfSignalMetadata builds a pdf section's signal keys FROM THE PRODUCTION
// CONSTANTS rather than from typed strings, then overrides the values with ones
// a heading really carries.
//
// THE DERIVATION IS THE POINT. A hand-typed key set supplies its own answer key:
// during planning a draft example read `chrome_page_repeat_count` and PASSED
// against a fixture carrying that exact misspelling. Ranging the constants makes
// the fixture disagree with a wrong spelling immediately.
//
// THE VALUES MATTER SEPARATELY. With every signal set to a uniform "1" the
// pre-existing help example filtering `font_ratio_to_body >= 1.15` matched
// nothing and the gate reported a live example as rot.
func pdfSignalMetadata() map[string]string {
	md := map[string]string{}
	for _, k := range classify.RawSignalKeys {
		md[k] = "1"
	}
	for _, k := range chunk.ChromeSignalKeys {
		md[k] = "1"
	}
	md[chunk.MetaKeyPageSpan] = "1"

	md[classify.SignalFontSizePt] = "16"
	md[classify.SignalBodyFontSizePt] = "10"
	md[classify.SignalFontRatioToBody] = "1.6"
	md[classify.SignalBoldFraction] = "1"
	md[classify.SignalItalicFraction] = "0"
	md[classify.SignalMonoFraction] = "0"
	md[classify.SignalLineCount] = "1"
	md[classify.SignalGapAbovePt] = "14"
	md[classify.SignalPageAvgGapPt] = "6"
	md[chunk.ChromeKeyPageRepeatCount] = "1"
	md[chunk.ChromeKeyRepeatShaped] = "false"
	return md
}

// pdfFixtureGraph mirrors pdfcollector's emitDocumentNode/emitChunk shape: a
// document root, TWO heading sections carrying the unconditional chunk keys plus
// the constant-derived signal keys, and three leaves beneath the first. The two
// sections share a heading on purpose — see the note at the second one.
func pdfFixtureGraph() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	sectionMD := pdfSignalMetadata()
	sectionMD["source"] = "pdf"
	sectionMD["position"] = "0"
	sectionMD["page_first"] = "10"
	sectionMD["page_last"] = "10"
	sectionMD["heading_level"] = "1"
	sectionMD["chunk_kind"] = "section"

	dupSectionMD := pdfSignalMetadata()
	maps.Copy(dupSectionMD, sectionMD)
	dupSectionMD["position"] = "1"

	nodes := []*knowledgev1.Node{
		// Content carries the Info-dict BLURB emitDocumentNode builds, and the
		// root carries no Description at all — it used to carry one here, a
		// field no collector writes on any node this fixture holds.
		//
		// TWO OF THESE VALUES ARE WRITTEN OUT AND ONE IS CITED, deliberately.
		// title_source CITES pdfcollector's exported constant because an
		// admitted corpus check forbids a bare literal at any site that stamps
		// that key, here included — the vocabulary is closed and the check is
		// the compiler it otherwise lacks. collector_schema_version and the
		// Content blurb stay written out, because their gate is
		// TestFixtureGraphs_MirrorTheCollectorsDeclaredValues, which compares
		// both against the collectors' own exported declarations: a written
		// value the gate compares is caught when it drifts, which a cited one
		// can only be trivially. The version drifted to "1" against a constant
		// of 3 before that gate existed, and this is what now catches it.
		{Id: "d1", Type: "document", SymbolName: "Designing Fixtures", Source: "pdf-collect",
			Content: "Designing Fixtures — A Fixture",
			Metadata: map[string]string{
				"source": "pdf", "path": "/tmp/fixture.pdf",
				"collector_schema_version": "3",
				"title_source":             pdfcollector.TitleSourceInfoDict,
				"title":                    "Designing Fixtures", "author": "A Fixture",
			}},
		// SymbolName AND Content both carry the heading, which is what emitChunk
		// writes for a section and what makes `body` resolve to it.
		{Id: "s1", Type: "section", SymbolName: "Event-Driven Services",
			Content: "Event-Driven Services", Source: "pdf-collect", Metadata: sectionMD},
		// s2 REPEATS s1's HEADING, and the repeat is the fixture's whole point:
		// a book whose title or a chapter heading appears twice is ordinary
		// furniture (a title page and a copyright page carry the same words),
		// and an emit keyed on a heading-derived name is refused outright on it.
		// It carries s1's signal metadata so every worked body's filters admit
		// both rows — a duplicate the filters dropped would prove nothing.
		{Id: "s2", Type: "section", SymbolName: "Event-Driven Services",
			Content: "Event-Driven Services", Source: "pdf-collect", Metadata: dupSectionMD},
		{Id: "p1", Type: "paragraph", Content: "A fixture paragraph under the heading.",
			Source: "pdf-collect", Metadata: map[string]string{
				"source": "pdf", "position": "0", "page_first": "10", "page_last": "10"}},
		{Id: "cb1", Type: "code_block", Content: "fixture := true", Source: "pdf-collect",
			Metadata: map[string]string{
				"source": "pdf", "position": "1", "page_first": "10", "page_last": "10",
				"chunk_kind": "code"}},
		{Id: "tb1", Type: "table", Content: "col | col", Source: "pdf-collect",
			Metadata: map[string]string{
				"source": "pdf", "position": "2", "page_first": "10", "page_last": "10",
				"chunk_kind": "table"}},
	}
	edges := []*knowledgev1.Edge{
		containsEdge("d1", "s1", 0),
		containsEdge("d1", "s2", 1),
		containsEdge("s1", "p1", 0),
		containsEdge("s1", "cb1", 1),
		containsEdge("s1", "tb1", 2),
	}
	return nodes, edges
}

// webPageFixtureNode mirrors emitPageNode: url, final_url, http_status,
// content_hash, uri and collector_schema_version unconditionally, plus title
// when the page has one.
//
// AND NO BODY, IN EITHER TEXT FIELD, which is the rest of the page node's shape
// since the page-level flatten was retired: emitPageNode constructs the node
// with Id, Type, SymbolName, Source and Metadata and nothing else. This fixture
// carried a Description until this change, and the docs guide's first example
// read `body := page.description` — a field populated here and empty against
// every real crawl, which no gate could see.
func webPageFixtureNode(id, url, uri, title string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, Type: "page", SymbolName: title, Source: "web-collect",
		Metadata: map[string]string{
			"url": url, "final_url": url, "http_status": "200",
			"content_hash": "deadbeef", "uri": uri,
			"collector_schema_version": "3",
			"title":                    title,
		},
	}
}

// webSectionFixtureNode mirrors emitSection: heading, depth, uri and position
// unconditionally, anchor when the section has one, heading_source from
// applyHeadingSignal, and applyCommonAttrs' tag/dom_depth/attr_source. Content
// carries the heading, which is what makes `node.body` resolve on a section.
func webSectionFixtureNode(id, heading, anchor, uri, tag string, depth, pos, domDepth int) *knowledgev1.Node {
	md := map[string]string{
		"heading":  heading,
		"depth":    strconv.Itoa(depth),
		"uri":      uri,
		"position": strconv.Itoa(pos),
	}
	if anchor != "" {
		md["anchor"] = anchor
	}
	md["heading_source"] = "tag"
	md["tag"] = tag
	md["dom_depth"] = strconv.Itoa(domDepth)
	md["attr_source"] = "own"
	return &knowledgev1.Node{
		Id: id, Type: "section", SymbolName: heading, Content: heading,
		Source: "web-collect", Metadata: md,
	}
}

// webParagraphFixtureNode mirrors emitParagraph: position, uri and text_length
// unconditionally, links_only ONLY on a links-only run, and on that run the text
// moves from Content to Description — which is the shape the real emitter
// writes and the reason `body` still reaches it.
func webParagraphFixtureNode(id, text, uri, tag string, pos, domDepth int, linksOnly bool) *knowledgev1.Node {
	md := map[string]string{
		"position":    strconv.Itoa(pos),
		"uri":         uri,
		"text_length": strconv.Itoa(len([]rune(text))),
	}
	if linksOnly {
		md["links_only"] = "true"
	}
	md["tag"] = tag
	md["dom_depth"] = strconv.Itoa(domDepth)
	md["attr_source"] = "own"
	content, description := text, ""
	if linksOnly {
		content, description = "", text
	}
	return &knowledgev1.Node{
		Id: id, Type: "paragraph", Content: content, Description: description,
		Source: "web-collect", Metadata: md,
	}
}

// webFixtureGraph assembles the three per-node-type helpers above into two
// pages that share a title, the first of them carrying two sections that share a
// heading.
//
// EACH NODE CARRIES THE KEYS ITS OWN EMITTER WRITES, and the split is what makes
// this gate mean what it claims. The recipe census is a graph-wide UNION, so a
// flattened fixture that stamps a key on any node at all admits an example whose
// filter names that key on a node type which never carries it — the filter then
// excludes nothing here AND refuses outright on a real document.
//
// THE LINKS-ONLY PARAGRAPH IS THE DISCRIMINATOR, not filler: with the nav-chrome
// example's links_only clause deleted the run returns the navigation strip too,
// and with it present the strip is gone.
//
// THE `references` EDGE IS REQUIRED, NOT DECORATIVE. The help's pre-existing
// canonical cross-emit body carries `traverse references out as $related`, and
// an edge type the source graph does not carry is REFUSED BEFORE THE WALK rather
// than traversed to nothing. The live twelve-factor graph carries 1175 of them,
// so this is the collector's real shape. The casing is exact, and so is the
// target — see the edge's own note below.
func webFixtureGraph() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	const pageURI = "https://example.com/guide"
	const pageTwoURI = "https://example.com/guide-2"
	nodes := []*knowledgev1.Node{
		webPageFixtureNode("pg", pageURI, pageURI, "A Fixture Guide"),
		// pg2 REPEATS pg's TITLE. Two pages of one site sharing a <title> is
		// ordinary (a paginated article, a translated mirror), and it is what
		// makes a page-keyed identity collide at the SELECT rather than only
		// inside a subtree — the canonical cross-ref pipeline and both clause
		// illustrations key on a page.
		webPageFixtureNode("pg2", pageTwoURI, pageTwoURI, "A Fixture Guide"),
		webSectionFixtureNode("w1", "Handling Failure Modes", "failure-modes",
			pageURI+"#failure-modes", "section", 1, 0, 3),
		// w2 REPEATS w1's HEADING under the SAME page: the section-level half
		// of the same class.
		webSectionFixtureNode("w2", "Handling Failure Modes", "failure-modes-2",
			pageURI+"#failure-modes-2", "section", 1, 2, 3),
		webSectionFixtureNode("wnav", "Navigation", "", pageURI, "nav", 1, 1, 2),
		webParagraphFixtureNode("wp1", "A fixture paragraph of real prose.",
			pageURI+"#failure-modes", "p", 0, 4, false),
		webParagraphFixtureNode("wp2", "Home About Contact", pageURI, "p", 0, 3, true),
	}
	edges := []*knowledgev1.Edge{
		containsEdge("pg", "w1", 0),
		containsEdge("pg", "wnav", 1),
		containsEdge("pg", "w2", 2),
		containsEdge("w1", "wp1", 0),
		containsEdge("wnav", "wp2", 0),
		// PAGE TO PAGE, which is the only references shape a crawl leaves behind:
		// emitLinks writes the edge from the page id to a `web:url:` placeholder
		// and resolveInternalLinks rewrites an internal one to the visited page's
		// node id. A section-target edge stood beside this one until this change
		// and no collector path produces it. The page target is also what the
		// canonical cross-ref pipeline needs: it emits a pattern per PAGE and
		// looks one up on the traversed row, so a section target misses on every
		// row and the gate cannot tell a working pipeline from a broken one.
		{FromId: "pg", ToId: "pg2", Type: string(kgtypes.EdgeReferences)},
	}
	return nodes, edges
}

// TestFixtureGraphs_CarryOnlyShapesTheCollectorsEmit is the fidelity gate on the
// two fixture graphs: it holds each one to what its collector actually leaves
// behind, on the properties a recipe example can be misled by.
//
// WHY A FIXTURE NEEDS A GATE AT ALL. Every other gate in this package reads rows
// off a run against these graphs, so a shape no collector emits makes a shipped
// example look like it works. Both were violated here, and one was load-bearing:
// the page node carried a Description and the docs guide's first example read
// `body := page.description` — populated on this fixture, empty against every
// real crawl, and nothing in the tree could see the difference.
//
// THE PRODUCTION SIDE IS GATED WHERE THE PRODUCTION IS, because the two packages
// cannot import each other's unexported emitters. The web package's
// TestEmit_PageIsItsChunksNotItsBody drives a real crawl and asserts the page
// node carries no body in Content and none in Description either; emitPageNode
// (collector/web/emit_nodes.go) builds the node from Id, Type, SymbolName,
// Source and Metadata alone, emitLinks (same file) writes a references edge to
// another PAGE id or to a `web:url:` placeholder that resolveInternalLinks
// rewrites to the visited page's id, and emitDocumentNode
// (collector/pdf/pdfcollector/emit.go) gives the pdf root a Content blurb and no
// Description. This is the fixture half of that agreement, asserted here and
// named on both sides.
func TestFixtureGraphs_CarryOnlyShapesTheCollectorsEmit(t *testing.T) {
	nodes, edges := webFixtureGraph()

	pages := map[string]bool{}
	for _, n := range nodes {
		if n.Type == "page" {
			pages[n.Id] = true
		}
	}
	// THE FLOOR, so a derivation that found nothing cannot report clean.
	if len(pages) < 2 {
		t.Fatalf("the web fixture carries %d page nodes, want at least the two that share a title — the assertions below would pass over an empty set", len(pages))
	}

	for _, n := range nodes {
		if n.Type != "page" {
			continue
		}
		if n.Content != "" || n.Description != "" {
			t.Errorf("web fixture page %q carries a body (Content=%q Description=%q); emitPageNode writes neither, so an example reading a page body renders populated here and empty on every real crawl",
				n.Id, n.Content, n.Description)
		}
		// ...and it still carries its identity, so "no body" is not being
		// satisfied by a page node that carries nothing at all.
		if n.SymbolName == "" || n.Metadata["uri"] == "" {
			t.Errorf("web fixture page %q lost its title or its address", n.Id)
		}
	}

	references := 0
	for _, e := range edges {
		if e.Type != string(kgtypes.EdgeReferences) {
			continue
		}
		references++
		if !pages[e.ToId] {
			t.Errorf("web fixture references edge %s -> %s targets a node that is not a page; emitLinks only ever targets another page id or a `web:url:` placeholder, so no crawl produces this edge",
				e.FromId, e.ToId)
		}
	}
	if references == 0 {
		t.Fatal("the web fixture carries no references edge, so the target assertion above ran over nothing — the help's cross-emit body needs one, and an absent edge type is refused before the walk")
	}

	pdfNodes, _ := pdfFixtureGraph()
	roots := 0
	for _, n := range pdfNodes {
		if n.Type != "document" {
			continue
		}
		roots++
		if n.Description != "" {
			t.Errorf("pdf fixture document root %q carries a Description %q; emitDocumentNode writes SymbolName, Content and Metadata and no Description",
				n.Id, n.Description)
		}
		if n.Content == "" {
			t.Errorf("pdf fixture document root %q carries no Content; emitDocumentNode writes BuildDocumentBlurb's Info-dict blurb there, and a root with neither field is not the shape either", n.Id)
		}
		if n.Metadata["title_source"] == "" {
			t.Errorf("pdf fixture document root %q carries no title_source; emitDocumentNode stamps it unconditionally", n.Id)
		}
	}
	if roots != 1 {
		t.Fatalf("the pdf fixture carries %d document roots, want exactly 1 — the assertions above ran over the wrong population", roots)
	}
	t.Logf("fixture shapes: %d web pages, %d references edges, %d pdf document root", len(pages), references, roots)
}
