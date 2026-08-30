// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// composition.go declares what a USABLE pdf harvest must contain. The invariant
// does not differ from the web collector's in PRINCIPLE, only in gating.
//
// BOTH TYPE LITERALS are the pdf emitter's own stable strings, returned by
// nodeTypeForChunk at emit.go:184-187 — "paragraph" for pdf.BlockParagraph and
// "code_block" for pdf.BlockCode. The file comment at emit.go:25-29 records that
// this bare-name vocabulary deliberately mirrors collector/web's so recipes stay
// source-agnostic, which is why both collectors key on the same two literals
// rather than each defining their own.
const (
	pdfParagraphType = "paragraph"
	pdfCodeBlockType = "code_block"
)

// AssertComposition implements collector.CompositionAsserter for the pdf
// collector. Quoting the ticket's rule verbatim: "SUBSTANTIVE CONTENT IS
// `paragraph` OR `code_block`. ... The pdf leg fires when `paragraph +
// code_block == 0`."
//
// IT NEEDS NO PAGE GATE, unlike the web leg. The pdf collector has exactly one
// mode: PDFCollector.Collect always calls emit(), which always emits the root
// "document" node plus one node per chunk. There is no second path that
// legitimately emits no document.
//
// IT NEEDS NO ZERO-NODE LEG either — the counterpart of the web collector's
// second leg — because that leg would be UNREACHABLE here: a pdf result always
// carries at least the document node, and an unreachable leg cannot be told
// apart from one that was never wired.
//
// `block` — nodeTypeForChunk's DEFAULT branch at emit.go:192-193 — is
// deliberately NOT counted as substantive. 23 of 24 pdf fixtures carry block: 1,
// so counting it would make this gate inert except on document-only PDFs. Every
// exclusion here is an inertness argument, not a taste argument.
//
// WHY code_block COUNTS: a document made entirely of code is a real harvest.
// testdata/corpus/rfc-7234-caching/source.pdf measures paragraph 0 with hundreds
// of code_block nodes, so a paragraph-only rule would declare a genuine harvest
// of an RFC "captured nothing usable" inside an error message naming its own
// code blocks.
func (c *PDFCollector) AssertComposition(comp collector.CollectComposition) error {
	if comp.NodesByType[pdfParagraphType]+comp.NodesByType[pdfCodeBlockType] == 0 {
		return fmt.Errorf("collect pdf %s: harvest captured nothing usable — zero paragraph and zero code_block, so the document yielded no substantive content: %s", comp.GraphName, comp.Render())
	}
	return nil
}
