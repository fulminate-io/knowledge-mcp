// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
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
func TestHelpRecipes_WorkedExamplesValidateAgainstAFixtureGraph(t *testing.T) {
	blocks := extractRecipeBlocks(helpRecipes)
	if len(blocks) != helpRecipesWorkedBodyCount {
		t.Fatalf("extracted %d worked recipe bodies, want exactly %d — an example left the extractor's reach (check its four-space indent) or two merged because the prose between them was dropped",
			len(blocks), helpRecipesWorkedBodyCount)
	}

	caller := helpFixtureCaller()
	for i, b := range blocks {
		t.Run(firstLine(b), func(t *testing.T) {
			pdfRows, pdfOut := runHelpFixtureExtract(t, caller, "pdf", b)
			webRows, webOut := runHelpFixtureExtract(t, caller, "web", b)
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

// pdfFixtureGraph mirrors pdfcollector's emitDocument/emitChunk shape: a
// document root, one heading section carrying the unconditional chunk keys plus
// the constant-derived signal keys, and three leaves beneath it.
func pdfFixtureGraph() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	sectionMD := pdfSignalMetadata()
	sectionMD["source"] = "pdf"
	sectionMD["position"] = "0"
	sectionMD["page_first"] = "10"
	sectionMD["page_last"] = "10"
	sectionMD["heading_level"] = "1"
	sectionMD["chunk_kind"] = "section"

	nodes := []*knowledgev1.Node{
		{Id: "d1", Type: "document", SymbolName: "Designing Fixtures", Source: "pdf-collect",
			Description: "a fixture document root",
			Metadata: map[string]string{
				"source": "pdf", "path": "/tmp/fixture.pdf",
				"collector_schema_version": "1",
				"title":                    "Designing Fixtures", "author": "A Fixture",
			}},
		// SymbolName AND Content both carry the heading, which is what emitChunk
		// writes for a section and what makes `body` resolve to it.
		{Id: "s1", Type: "section", SymbolName: "Event-Driven Services",
			Content: "Event-Driven Services", Source: "pdf-collect", Metadata: sectionMD},
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
		containsEdge("s1", "p1", 0),
		containsEdge("s1", "cb1", 1),
		containsEdge("s1", "tb1", 2),
	}
	return nodes, edges
}

// webPageFixtureNode mirrors emitPageNode: url, final_url, http_status,
// content_hash, uri and collector_schema_version unconditionally, plus title
// when the page has one.
func webPageFixtureNode(id, url, uri, title string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, Type: "page", SymbolName: title, Source: "web-collect",
		Description: "a fixture page's flattened body",
		Metadata: map[string]string{
			"url": url, "final_url": url, "http_status": "200",
			"content_hash": "deadbeef", "uri": uri,
			"collector_schema_version": "1",
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

// webFixtureGraph assembles the three per-node-type helpers above into one page.
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
// so this is the collector's real shape. The casing is exact.
func webFixtureGraph() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	const pageURI = "https://example.com/guide"
	nodes := []*knowledgev1.Node{
		webPageFixtureNode("pg", pageURI, pageURI, "A Fixture Guide"),
		webSectionFixtureNode("w1", "Handling Failure Modes", "failure-modes",
			pageURI+"#failure-modes", "section", 1, 0, 3),
		webSectionFixtureNode("wnav", "Navigation", "", pageURI, "nav", 1, 1, 2),
		webParagraphFixtureNode("wp1", "A fixture paragraph of real prose.",
			pageURI+"#failure-modes", "p", 0, 4, false),
		webParagraphFixtureNode("wp2", "Home About Contact", pageURI, "p", 0, 3, true),
	}
	edges := []*knowledgev1.Edge{
		containsEdge("pg", "w1", 0),
		containsEdge("pg", "wnav", 1),
		containsEdge("w1", "wp1", 0),
		containsEdge("wnav", "wp2", 0),
		{FromId: "pg", ToId: "w1", Type: string(kgtypes.EdgeReferences)},
	}
	return nodes, edges
}
