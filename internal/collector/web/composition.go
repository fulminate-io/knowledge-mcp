// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// composition.go declares what a USABLE web harvest must contain, so a crawl
// that captured only navigation chrome stops reading exactly like a successful
// one. A count of nodes written is to a harvest what a green test run is to a
// check that never executed.
//
// THE FOUR TYPE-NAME LITERALS below are the emitters' own stable strings, and
// records.go:12-15 records that they are part of the on-disk contract and must
// never change:
//
//	"page"       — set in emitPageNode, collector/web/emit_nodes.go
//	"paragraph"  — returned by paragraphRecord.recordKind(), records.go
//	"code_block" — returned by codeBlockRecord.recordKind(), records.go
//	"raw_html"   — set in emitRawHTML, collector/web/emit_nodes_rawhtml.go
//
// Do not introduce a parallel vocabulary for them.
const (
	webPageType      = "page"
	webParagraphType = "paragraph"
	webCodeBlockType = "code_block"
	webRawHTMLType   = "raw_html"
)

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
	substantive := comp.NodesByType[webParagraphType] + comp.NodesByType[webCodeBlockType]
	if pages > 0 && substantive == 0 {
		return fmt.Errorf("collect web %s: harvest captured nothing usable — %d page node(s) but zero paragraph and zero code_block, so the crawl captured only chrome: %s", comp.GraphName, pages, comp.Render())
	}

	rawHTML := comp.NodesByType[webRawHTMLType]
	if pages > 0 && rawHTML < pages {
		return fmt.Errorf("collect web %s: page bodies were not retained — %d page node(s) but only %d raw_html node(s), so the captured HTML is incomplete: %s", comp.GraphName, pages, rawHTML, comp.Render())
	}
	return nil
}
