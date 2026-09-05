// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// composition.go declares what a USABLE web harvest must contain, so a crawl
// that captured only navigation chrome stops reading exactly like a successful
// one. A count of nodes written is to a harvest what a green test run is to a
// check that never executed.
//
// EVERY TYPE-NAME LITERAL DECLARED BELOW is one of the emitters' own stable
// strings, and records.go:12-15 records that they are part of the on-disk
// contract and must never change. The list is the const block itself, so
// adding an emitter type here does not leave a count behind to go stale:
//
//	"page"       — set in emitPageNode, collector/web/emit_nodes.go
//	"paragraph"  — returned by paragraphRecord.recordKind(), records.go
//	"code_block" — returned by codeBlockRecord.recordKind(), records.go
//	"table"      — returned by tableRecord.recordKind(), records.go
//	"blockquote" — returned by quoteRecord.recordKind(), records.go
//	"list_item"  — returned by listItemRecord.recordKind(), records.go
//	"raw_html"   — set in emitRawHTML, collector/web/emit_nodes_rawhtml.go
//
// Do not introduce a parallel vocabulary for them.
const (
	webPageType       = "page"
	webParagraphType  = "paragraph"
	webCodeBlockType  = "code_block"
	webTableType      = "table"
	webBlockquoteType = "blockquote"
	webListItemType   = "list_item"
	webRawHTMLType    = "raw_html"
)

// webContentTypes is the set of emitted node types that count as a harvest's
// TEXT for the substantive-content leg.
//
// ENUMERATED FROM THE EMITTER, not from the vocabulary at large, and the census
// is taken over the WHOLE PACKAGE rather than over one file. SIX sites assign
// Content: five in emit_nodes_records.go — emitParagraph, emitCodeBlock, the
// item arm of emitList, emitTable and emitQuote — and a sixth in emit_nodes.go,
// where emitSection writes `Content: sec.Heading`. Every one of the six appears
// below with its disposition. The types that carry no Content are absent for a
// reason rather than by oversight — "list" is a container whose text lives on
// its items, "link" and "image" carry their payload in metadata, and "page" no
// longer carries a body at all since a page became its chunks.
//
// "section" FILLS Content AND IS EXCLUDED ANYWAY, which is the one exclusion
// here that rests on an argument rather than on an observation. emitSection
// writes the heading into Content deliberately, and its own doc says why: with
// the page flatten retired the heading text exists on no other node, and
// Content is the field every content composer reads. It is still not a
// harvest's TEXT. A heading is the document naming its own parts — structure —
// not the prose those parts contain, so a page whose section headings all
// landed and whose paragraphs, code blocks, tables and blockquotes all did not
// is a table of contents. That is precisely the harvest this leg exists to
// refuse, and counting section would let a crawl that recovered nothing but an
// outline report plain success.
//
// "list_item" IS ADMITTED ON EXACTLY THE FOOTING A TABLE IS, and the reason it
// was once left out is worth keeping, because it names the condition that had
// to change. It was excluded because the collector had NO VERDICT separating a
// menu entry from a bullet of prose: the commonest chrome on the web is a
// navigation <ul> of bare anchors, handleList emits each of those anchors as a
// list_item carrying its label in Content, and counting them silenced this leg
// for nav-only harvests — MEASURED at the time against the package's own
// known-positive control, a real crawl of a nav list in composition_test.go,
// which went from a reported failure to plain success.
//
// That condition no longer holds. classifyList (parse_dom_list_helpers.go)
// supplies the verdict, stamping list_nav on every list and inheriting it onto
// every item, and countRetainedChrome (collector.go) subtracts nav-list items
// from this sum the way it already subtracts layout tables. A table was
// admissible because classifyTable separated scaffolding from data; a list_item
// is admissible now for the same reason and by the same two-part mechanism —
// a verdict at the walker, a subtraction at the census.
//
// THE FORWARD RULE IS OVER THE CENSUS, NOT OVER A COUNT. Every emitter that
// fills Content is listed above with its disposition, so a NEW Content-filling
// emitter is added to that list with the same question asked of it — can this
// type be chrome, and if so what verdict separates the two? — and the census is
// re-taken over the package, never over the one file the new emitter happens to
// live in. A rule phrased as "the next one" goes stale the moment it lands; a
// rule phrased over the census does not.
var webContentTypes = []string{
	webParagraphType,
	webCodeBlockType,
	webTableType,
	webBlockquoteType,
	webListItemType,
}

// AssertComposition implements collector.CompositionAsserter for the web
// collector. Three legs, in this order.
//
// THE ZERO-NODE LEG. A harvest that emitted no nodes at all is a failed harvest.
// It is a SEPARATE, INDEPENDENT leg because the page gate below structurally
// cannot cover it — zero nodes means zero pages, so the gate would not apply —
// and it is reachable today: collector/web/collector.go returns a nil node slice
// and NO error when every fetch fails.
//
// THE PAGE-GATED SUBSTANTIVE-CONTENT LEG. Quoting the ticket's rule verbatim:
// "SUBSTANTIVE CONTENT IS `paragraph` OR `code_block`. The web leg fires when a
// harvest emitted at least one `page` node and `paragraph + code_block == 0`."
//
// THE SUM IS WIDER THAN THAT QUOTE, and the restatement is mine rather than the
// ticket's: substantive content is any node type whose Content this collector
// FILLS — every member of webContentTypes, whose membership the census above
// settles type by type. The ticket's two-type rule was written
// when a page carried a flattened body, so paragraph and code_block were the
// only chunks whose text was not also on the page node. This same ticket
// retired that flatten and made a data table's cells the SOLE carrier of their
// text, which turned the narrower sum into an under-count: a crawl of a
// table-dominant site — spec tables, label/value grids, reference matrices —
// was refused as "captured only chrome" with its text sitting in the graph,
// and refusing a good harvest is a worse failure than the one the leg guards
// against. The rule's INTENT is unchanged and is what is implemented here:
// fire when the harvest captured pages but no text.
//
// RETAINED CHROME IS SUBTRACTED FROM THAT SUM, and the subtraction is what
// keeps the leg firing after the walker began retaining navigation strips. A
// strip is emitted as a node carrying the `paragraph` TYPE, because the graph
// keeps its node vocabulary — so without this, a page holding nothing but
// navigation counts one paragraph and reads as a substantive harvest, which is
// precisely the state this leg exists to catch. The count rides
// CollectResult.NonSubstantiveNodes from the collector that knows what the
// class means; no collector-generic code reads a web metadata key.
//
// The comparison is `<= 0` rather than `== 0` because the sum is now a
// subtraction: reading it as an exact zero would let a harvest whose chrome
// count exceeded its paragraph count slip past the leg entirely.
//
// WHY THE PAGE GATE, and why it is not a per-site carve-out: processURL
// (crawl_process.go:33-36) short-circuits github URLs into materializeGithub,
// which records no pageRecord, so those harvests legitimately emit
// github_repo/file/function_declaration/language or document nodes and NO page
// node — the HTML path is the sole producer. The gate excludes only harvests
// that crawled no HTML page; it has no allow-list and no per-site knowledge.
//
// WHY code_block COUNTS AS SUBSTANTIVE: a page made entirely of code is a real
// harvest. A headings-plus-<pre> documentation page measures page 1, code_block
// 3, section 4, paragraph 0, and a paragraph-only rule would declare it empty.
//
// THE GATE'S RESOLUTION IS PER-HARVEST, NOT PER-PAGE. One qualifying node
// anywhere in an N-page crawl silences it for the whole crawl, and N-page crawls
// are the norm — CrawlOptions.ApplyDefaults (options.go:92-98) deliberately
// leaves MaxDepth and MaxPages unbounded. So this catches TOTAL extraction
// collapse, not partial: a 40-page crawl where 39 pages yield only chrome and
// one yields prose passes. Per-page resolution is out of scope by decision, not
// by oversight, and the guard's value depends on knowing what it does not catch.
//
// THE PAGE-GATED RETENTION LEG, added last. Faithful capture means every page
// that reached the graph brought its response bytes with it, so a harvest with
// page nodes but fewer raw_html nodes than pages has silently stopped
// retaining. It reuses the SAME `pages` measurement and the SAME page gate as
// the leg above — github harvests emit no page node and no raw_html node, and
// are excluded here by the identical mechanism, not by a per-site carve-out.
//
// ORDER MATTERS: this leg runs LAST so a harvest that captured only chrome
// still reports the chrome failure, which is the more informative diagnosis,
// rather than being reported as a retention failure.
//
// The comparison is `< pages` rather than `!= pages` deliberately: raw_html
// nodes in excess of pages would require a producer that does not exist, and
// a strict inequality keeps the leg about the failure it was written for —
// retention silently not happening — instead of about an arrangement no code
// can currently produce.
func (c *WebCollector) AssertComposition(comp collector.CollectComposition) error {
	if comp.TotalNodes == 0 {
		return fmt.Errorf("collect web %s: harvest captured nothing usable — the crawl emitted no nodes at all (%s)", comp.GraphName, comp.Render())
	}

	pages := comp.NodesByType[webPageType]
	substantive := 0
	for _, t := range webContentTypes {
		substantive += comp.NodesByType[t]
	}
	substantive -= comp.NonSubstantiveNodes
	if pages > 0 && substantive <= 0 {
		return fmt.Errorf("collect web %s: harvest captured nothing usable — %d page node(s) but no node carrying text (%s), so the crawl captured only chrome: %s",
			comp.GraphName, pages, strings.Join(webContentTypes, "/"), comp.Render())
	}

	rawHTML := comp.NodesByType[webRawHTMLType]
	if pages > 0 && rawHTML < pages {
		return fmt.Errorf("collect web %s: page bodies were not retained — %d page node(s) but only %d raw_html node(s), so the captured HTML is incomplete: %s", comp.GraphName, pages, rawHTML, comp.Render())
	}
	return nil
}
