// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_practice_linkage.go holds the practice + linkage search renderers the
// InterceptQueryPracticeLinkage composer (cmd/knowledge/internal/tools) consumes.
// They live in the engine package so the linkage renderer can reuse the
// already-client-side proxy annotation helpers (proxyMetadataAnnotation,
// traversalNodeName) rather than re-implementing proxy-target formatting.

// RenderPracticeResults renders practice-graph search results — a port of the
// server's since-removed formatPracticeResults: the
// "## <LANG> Best Practices" header + per-result importance/category lines.
//
// searchMode is the ALWAYS-ON arm disclosure ("vector+text", "vector",
// "BM25-only"), rendered as the same "_search mode: …_" footer renderText emits so
// the two search surfaces read alike. It is not conditional on the result set: a
// caller cannot tell a degraded hybrid search from a healthy one by looking at
// rows, so the label has to be present when results ARE returned — that is
// precisely the case where the degrade is invisible. Empty prints no footer, for
// callers that have no arm information to report.
func RenderPracticeResults(lang, query string, results []SearchResult, searchMode string) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s Best Practices — %d results for \"%s\"\n\n", capFirst(lang), len(results), query)
	for i, r := range results {
		writePracticeHit(&sb, i, r, "")
	}
	writeSearchModeFooter(&sb, searchMode)
	return kgtools.TextResult(sb.String())
}

// writeSearchModeFooter appends the arm-disclosure footer both practice renderers
// emit. Spelled once so the two cannot drift, and matched to renderText's wording
// so a reader learns one form rather than three.
func writeSearchModeFooter(sb *strings.Builder, searchMode string) {
	if searchMode == "" {
		return
	}
	fmt.Fprintf(sb, "\n_search mode: %s_\n", searchMode)
}

// writePracticeHit writes the per-hit render block shared by RenderPracticeResults
// (single-language) and RenderPracticeFanOut (cross-graph): the
// "### <n>. <symbol> [importance] (category)" line, the score+content line, and the
// "ID: … | Status: …" line. When graphTag is non-empty it is appended to the header
// as " — <graphTag>" so a merged fan-out hit names its source practice graph; the
// single-language renderer passes "" and the line is byte-identical to the pre-extract
// shape. Both renderers emit the same per-hit lines for the same SearchResult.
func writePracticeHit(sb *strings.Builder, idx int, r SearchResult, graphTag string) {
	n := r.Node
	category := kgtypes.Value(n, "category")
	importance := kgtypes.Value(n, "importance")
	fmt.Fprintf(sb, "### %d. %s", idx+1, n.SymbolName)
	if importance != "" {
		fmt.Fprintf(sb, " [%s]", importance)
	}
	if category != "" {
		fmt.Fprintf(sb, " (%s)", category)
	}
	if graphTag != "" {
		fmt.Fprintf(sb, " — %s", graphTag)
	}
	fmt.Fprintf(sb, "\n%.2f — %s\n", r.Score, n.Content)
	fmt.Fprintf(sb, "ID: %s | Status: %s\n\n", n.Id, n.Status)
}

// PracticeFanOutHit is a practice search hit tagged with the practice graph
// (language) it came from. The scatter-gather fan-out (composePracticeSearchFanOut)
// emits these so the merged renderer (RenderPracticeFanOut) can attribute each
// hit to its source graph in the markdown output. The json arm no longer needs
// this wrapper — SearchResult.Graph/GraphInstance now carries the per-result
// source-graph identity directly, stamped at hydrate time — so the
// wrapper is a markdown-render concern only.
type PracticeFanOutHit struct {
	Graph  string
	Result SearchResult
}

// RenderPracticeFanOut renders a merged, score-ranked practice search across N
// practice graphs. It emits a "Searched N practice graphs" header naming the
// graphs searched, then one entry per hit tagged with its source graph via the
// shared writePracticeHit helper RenderPracticeResults also calls — so the two
// renderers emit identical per-hit lines for the same SearchResult.
//
// searchMode is the same ALWAYS-ON arm disclosure RenderPracticeResults renders,
// and it matters MORE here: the fan-out embeds the query once and reuses that one
// vector for every graph, so a single failed embed silently degrades the whole
// cross-graph ranking rather than one graph's.
func RenderPracticeFanOut(query string, graphs []string, hits []PracticeFanOutHit, searchMode string) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Searched %d practice graphs (%s) — %d results for \"%s\"\n\n",
		len(graphs), strings.Join(graphs, ", "), len(hits), query)
	for i, h := range hits {
		writePracticeHit(&sb, i, h.Result, h.Graph)
	}
	writeSearchModeFooter(&sb, searchMode)
	return kgtools.TextResult(sb.String())
}

// capFirst upper-cases the first rune (port of the server's
// strings.ToUpper(lang[:1])+lang[1:] idiom, guarded for the empty string).
func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
