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
// server formatPracticeResults (cmd/knowledge-server/tools/tools_query_practice.go):
// the "## <LANG> Best Practices" header + per-result importance/category lines.
func RenderPracticeResults(lang, query string, results []SearchResult) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s Best Practices — %d results for \"%s\"\n\n", capFirst(lang), len(results), query)
	for i, r := range results {
		writePracticeHit(&sb, i, r, "")
	}
	return kgtools.TextResult(sb.String())
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
// emits these so the merged renderer can attribute each hit to its source graph —
// engine.SearchResult itself carries no graph field.
type PracticeFanOutHit struct {
	Graph  string
	Result SearchResult
}

// RenderPracticeFanOut renders a merged, score-ranked practice search across N
// practice graphs. It emits a "Searched N practice graphs" header naming the
// graphs searched, then one entry per hit tagged with its source graph via the
// shared writePracticeHit helper RenderPracticeResults also calls — so the two
// renderers emit identical per-hit lines for the same SearchResult.
func RenderPracticeFanOut(query string, graphs []string, hits []PracticeFanOutHit) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Searched %d practice graphs (%s) — %d results for \"%s\"\n\n",
		len(graphs), strings.Join(graphs, ", "), len(hits), query)
	for i, h := range hits {
		writePracticeHit(&sb, i, h.Result, h.Graph)
	}
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
