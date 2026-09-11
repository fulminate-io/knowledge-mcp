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
// server's since-removed formatPracticeResults: the "## Best Practices" header +
// per-result importance/category lines.
//
// THE COUNT IS PLURALIZED. The header read "%d results" unconditionally, so a
// single hit rendered "1 results" — the search that returns exactly one match is
// the common case on a narrow query, so it was the wording an operator saw most.
//
// THE HEADER NAMES NO GRAPH, AND IT NAMES NO FAMILY EITHER. It used to
// interpolate the caller's `language` for the pre-singleton graph the search
// read; there is one combined graph now and the selector is refused, so an
// interpolated name would have been the empty string on every call. The
// intermediate wording was "## Practice Best Practices", which stutters in
// operator-facing output — the caller named the family in the call, so repeating
// it in the body adds nothing. "## Practice Graph" was the alternative, matching
// the stats arm's header; it is not used HERE because this body is a RESULT LIST
// rather than a description of a graph, and a header that names the graph would
// mislabel it.
//
// searchMode is the ALWAYS-ON arm disclosure ("vector+text", "vector",
// "BM25-only"), rendered as the same "_search mode: …_" footer renderText emits so
// the two search surfaces read alike. It is not conditional on the result set: a
// caller cannot tell a degraded hybrid search from a healthy one by looking at
// rows, so the label has to be present when results ARE returned — that is
// precisely the case where the degrade is invisible. Empty prints no footer, for
// callers that have no arm information to report.
func RenderPracticeResults(query string, results []SearchResult, searchMode string) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Best Practices — %d result%s for %q\n\n",
		len(results), pluralSuffix(len(results)), query)
	for i, r := range results {
		writePracticeHit(&sb, i, r)
	}
	writeSearchModeFooter(&sb, searchMode)
	return kgtools.TextResult(sb.String())
}

// pluralSuffix returns "s" when n != 1, so a single hit renders "1 result"
// rather than "1 results". It keeps the header grammatical without a ternary in
// the format code.
//
// IT IS A SECOND COPY, and the import direction is why. The identical helper
// lives in cmd/knowledge/internal/tools (tools_text_helpers.go) with the same
// doc, and this package cannot reach it: package tools imports package engine so
// its per-graph composers can consume these renderers, so engine importing tools
// back would be a cycle. This is the same situation formatBytes records beside
// that helper, for the module boundary rather than the package one.
func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// writeSearchModeFooter appends the arm-disclosure footer the practice renderer
// emits, matched to renderText's wording so a reader learns one form rather than
// two.
func writeSearchModeFooter(sb *strings.Builder, searchMode string) {
	if searchMode == "" {
		return
	}
	fmt.Fprintf(sb, "\n_search mode: %s_\n", searchMode)
}

// writePracticeHit writes one result's render block: the
// "### <n>. <symbol> [importance] (category)" line, the score+content line, and the
// "ID: … | Status: …" line.
//
// THE PER-GRAPH TAG WENT WITH THE FAN-OUT RENDERER. A merged cross-graph search
// once appended " — <graph>" to the header so a hit named its source practice
// graph; there is one combined graph, so there is no second renderer and nothing
// to attribute. The same removal took capFirst, which title-cased the language the
// header interpolated.
func writePracticeHit(sb *strings.Builder, idx int, r SearchResult) {
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
	fmt.Fprintf(sb, "\n%.2f — %s\n", r.Score, n.Content)
	fmt.Fprintf(sb, "ID: %s | Status: %s\n\n", n.Id, n.Status)
}
