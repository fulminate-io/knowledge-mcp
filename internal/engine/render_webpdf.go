// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_webpdf.go renders ranked results over a raw document graph — the web
// and pdf families — with the locality context that makes a hit actionable.
//
// WITHOUT THE CONTEXT LINE A WEB PARAGRAPH HIT IS NAMELESS. A web paragraph node
// carries no SymbolName and no Description, so the generic renderer's
// SymbolName -> Description -> Id fallback would print a bare hex id and the
// reader would learn nothing about where the match sits. The heading lives on
// the parent section node, which is why the caller resolves it and passes it in.
// PDF chunks already carry their own locality in page metadata, so the two
// families need different context and get it from the same renderer.

// rawGraphBodyCap bounds the per-hit body snippet. Wide enough to read a
// paragraph's opening sentence, narrow enough that ten hits stay scannable.
const rawGraphBodyCap = 300

// RawGraphHit pairs a ranked result with the containing heading resolved for it.
// The heading is derived per query rather than stored on the node, so it travels
// beside the result instead of inside it.
type RawGraphHit struct {
	Result        SearchResult
	ParentHeading string
}

// RenderRawGraphResults renders raw-graph hits as markdown: per hit a ranked
// header, a context line locating it in the document, a bounded body snippet and
// the node id.
//
// THE FOOTER IS AN UNCONDITIONAL DISCLOSURE, not a status line. Raw graphs carry
// no vectors, so no vector arm ran and none could have; saying so on every
// render is what stops a reader taking these rows for hybrid-search results that
// happened to return text matches. The literal is spelled here rather than
// called from the tools-side label helper because engine must not import tools —
// these renderers were relocated into engine precisely to avoid that cycle. The
// tools-side arms emit the same spelling for their JSON arm, which is where the
// two are kept in agreement.
func RenderRawGraphResults(graph, name, query string, hits []RawGraphHit) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s/%s — %d results for \"%s\"\n\n", graph, name, len(hits), query)
	for i, h := range hits {
		writeRawGraphHit(&sb, i, graph, name, h)
	}
	sb.WriteString("\n_search mode: BM25-only_\n")
	return kgtools.TextResult(sb.String())
}

// writeRawGraphHit writes one hit's block. Every metadata read goes through
// kgtypes.Value, so a key an emitter did not write renders as absent rather than
// as an empty field.
func writeRawGraphHit(sb *strings.Builder, idx int, graph, name string, h RawGraphHit) {
	n := h.Result.Node
	// Label falls back through the three things that can name a hit, ending at
	// the node type — never at a bare id, which names nothing.
	label := n.GetSymbolName()
	if label == "" {
		label = h.ParentHeading
	}
	if label == "" {
		label = n.GetType()
	}
	fmt.Fprintf(sb, "### %d. [%s] %s — %.2f\n", idx+1, n.GetType(), label, h.Result.Score)

	context := []string{graph + "/" + name}
	if h.ParentHeading != "" {
		context = append(context, "under: "+h.ParentHeading)
	}
	if url := kgtypes.Value(n, "url"); url != "" {
		context = append(context, url)
	}
	if anchor := kgtypes.Value(n, "anchor"); anchor != "" {
		context = append(context, "#"+anchor)
	}
	if pages := rawGraphPageSpan(n); pages != "" {
		context = append(context, pages)
	}
	fmt.Fprintf(sb, "%s\n", strings.Join(context, " | "))

	if body := n.GetContent(); body != "" {
		fmt.Fprintf(sb, "%s\n", truncate(body, rawGraphBodyCap))
	} else if body := n.GetDescription(); body != "" {
		fmt.Fprintf(sb, "%s\n", truncate(body, rawGraphBodyCap))
	}
	fmt.Fprintf(sb, "ID: %s\n\n", n.GetId())
}

// rawGraphPageSpan renders a pdf chunk's page locality, or "" when the node
// carries none — which is every web node, since only the pdf emitter writes
// these keys.
func rawGraphPageSpan(n *knowledgev1.Node) string {
	first := kgtypes.Value(n, "page_first")
	last := kgtypes.Value(n, "page_last")
	switch {
	case first == "" && last == "":
		return ""
	case last == "" || last == first:
		return "p. " + first
	case first == "":
		return "p. " + last
	default:
		return "pp. " + first + "-" + last
	}
}
