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
		n := r.Node
		category := kgtypes.Value(n, "category")
		importance := kgtypes.Value(n, "importance")
		fmt.Fprintf(&sb, "### %d. %s", i+1, n.SymbolName)
		if importance != "" {
			fmt.Fprintf(&sb, " [%s]", importance)
		}
		if category != "" {
			fmt.Fprintf(&sb, " (%s)", category)
		}
		fmt.Fprintf(&sb, "\n%.2f — %s\n", r.Score, n.Content)
		fmt.Fprintf(&sb, "ID: %s | Status: %s\n\n", n.Id, n.Status)
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

// RenderLinkageSearch renders linkage-graph search results — a port of the
// server formatLinkageSearchResults. Reuses the engine proxy annotation helpers
// (traversalNodeName for the display name, proxyMetadataAnnotation for the
// proxy-target annotation) per the no-reimplementation criterion.
func RenderLinkageSearch(query string, results []SearchResult) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Linkage — %d results for %q\n\n", len(results), query)
	for i, r := range results {
		n := r.Node
		name := traversalNodeName(n)
		annotation := proxyMetadataAnnotation(n)
		fmt.Fprintf(&sb, "### %d. %s", i+1, name)
		if annotation != "" {
			fmt.Fprintf(&sb, " %s", annotation)
		}
		fmt.Fprintf(&sb, "\n%.2f — %s\n\n", r.Score, n.Id)
	}
	return kgtools.TextResult(sb.String())
}
