// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_code_search.go ports the code-search result renderers from the server
// codegraph/format.go + search.go. The composer (cmd/knowledge/internal/tools)
// drives the generic RETURN_MODE_SEARCH Execute(s), resolves CodeResolvedResults,
// and renders via these ports. group_by_file groups the RETURNED results by file
// (the server's full-graph "(N of M symbols)" total needs a server DB walk — the
// client renders the results-only "(N symbols)" shape, matching the server's own
// total==0 fallback). The staleness "Indexed N ago" line is NOT reconstructable
// client-side (no graph-meta carrier); it degrades to
// empty, matching StalenessInfoWith's own degrade-on-missing-meta.

// CodeResolvedResult mirrors the server CodeResolvedResult: a search hit + its
// resolved node + repo tag (empty for single-repo).
type CodeResolvedResult struct {
	Score float64
	Node  *knowledgev1.Node
	Found bool
	Repo  string
}

// WriteCodeSearchHeader ports WriteSearchHeader.
func WriteCodeSearchHeader(sb *strings.Builder, queries []string, resultCount int, modeLabel string) {
	if len(queries) == 1 {
		fmt.Fprintf(sb, "Found %d results for %q (mode: %s):\n\n", resultCount, queries[0], modeLabel)
	} else {
		fmt.Fprintf(sb, "Found %d results for %d queries (mode: %s):\n", resultCount, len(queries), modeLabel)
		fmt.Fprintf(sb, "Queries: %s\n\n", strings.Join(queries, " | "))
	}
}

// WriteCodePerQuerySearchHeader ports WritePerQuerySearchHeader.
func WriteCodePerQuerySearchHeader(sb *strings.Builder, queries []string, perQueryCounts []int, modeLabel string) {
	if len(queries) == 1 {
		WriteCodeSearchHeader(sb, queries, perQueryCounts[0], modeLabel)
		return
	}
	total := 0
	for _, c := range perQueryCounts {
		total += c
	}
	fmt.Fprintf(sb, "Found %d results for %d queries (mode: %s):\n\n", total, len(queries), modeLabel)
}

// FormatCodeFlatResults ports the server FormatCodeFlatResults.
func FormatCodeFlatResults(sb *strings.Builder, resolved []CodeResolvedResult, includeSource bool) {
	for i, rr := range resolved {
		if !rr.Found {
			fmt.Fprintf(sb, "%d. %s (score: %.4f) [node metadata missing]\n\n", i+1, rr.Node.Id, rr.Score)
			continue
		}
		n := rr.Node
		sb.WriteString("---\n\n")
		fmt.Fprintf(sb, "### %d. %s (%s) — %s:%d (score: %.4f)\n", i+1, CodeDisplayName(n), n.Type, n.FilePath, n.StartLine, rr.Score)
		writeCodeResultBody(sb, n, includeSource)
	}
}

// FormatCodeCrossRepoFlatResults ports the server FormatCrossRepoFlatResults.
func FormatCodeCrossRepoFlatResults(sb *strings.Builder, resolved []CodeResolvedResult, includeSource bool) {
	for i, rr := range resolved {
		if !rr.Found {
			fmt.Fprintf(sb, "%d. [%s] %s (score: %.4f) [node metadata missing]\n\n", i+1, rr.Repo, rr.Node.Id, rr.Score)
			continue
		}
		n := rr.Node
		sb.WriteString("---\n\n")
		repoTag := ""
		if rr.Repo != "" {
			repoTag = "[" + rr.Repo + "] "
		}
		fmt.Fprintf(sb, "### %d. %s%s (%s) — %s:%d (score: %.4f)\n", i+1, repoTag, CodeDisplayName(n), n.Type, n.FilePath, n.StartLine, rr.Score)
		writeCodeResultBody(sb, n, includeSource)
	}
}

// writeCodeResultBody emits the Summary/Signature/source block shared by the
// flat + cross-repo renderers.
func writeCodeResultBody(sb *strings.Builder, n *knowledgev1.Node, includeSource bool) {
	if n.SymbolName == "" && n.Summary != "" {
		// Summary already used as display name.
	} else if n.Summary != "" {
		fmt.Fprintf(sb, "Summary: %s\n", n.Summary)
	}
	if n.Signature != "" {
		fmt.Fprintf(sb, "Signature: `%s`\n", n.Signature)
	}
	if includeSource && n.Content != "" {
		fmt.Fprintf(sb, "\n```%s\n%s\n```\n\n", n.Language, n.Content)
	}
}

// FormatCodePerQueryResults ports FormatPerQueryResults.
func FormatCodePerQueryResults(sb *strings.Builder, queries []string, perQuery [][]CodeResolvedResult, includeSource bool) {
	if len(queries) == 1 {
		FormatCodeFlatResults(sb, perQuery[0], includeSource)
		return
	}
	for i, q := range queries {
		fmt.Fprintf(sb, "## Query %d: %q (%d results)\n\n", i+1, q, len(perQuery[i]))
		FormatCodeFlatResults(sb, perQuery[i], includeSource)
		if i < len(queries)-1 {
			sb.WriteString("\n")
		}
	}
}

// FormatCodePerQueryCrossRepo ports FormatPerQueryCrossRepo.
func FormatCodePerQueryCrossRepo(sb *strings.Builder, queries []string, perQuery [][]CodeResolvedResult, includeSource bool) {
	if len(queries) == 1 {
		FormatCodeCrossRepoFlatResults(sb, perQuery[0], includeSource)
		return
	}
	for i, q := range queries {
		fmt.Fprintf(sb, "## Query %d: %q (%d results)\n\n", i+1, q, len(perQuery[i]))
		FormatCodeCrossRepoFlatResults(sb, perQuery[i], includeSource)
		if i < len(queries)-1 {
			sb.WriteString("\n")
		}
	}
}

// FormatCodeGroupByFile groups the RETURNED results by file (best-score order),
// rendering the "### <file> (N symbols)" + per-symbol lines. This is the
// client-side results-only variant of FormatGroupByFileOnGraph — the server's
// "(N of M symbols)" total needs a full-graph DB walk (server-side); the client
// renders the same shape the server's total==0 fallback produces.
func FormatCodeGroupByFile(sb *strings.Builder, resolved []CodeResolvedResult) {
	type fileGroup struct {
		filePath  string
		bestScore float64
		symbols   []CodeResolvedResult
	}
	var order []string
	groups := map[string]*fileGroup{}
	for _, rr := range resolved {
		fp := rr.Node.FilePath
		if !rr.Found || fp == "" {
			fp = "(unknown)"
		}
		g, ok := groups[fp]
		if !ok {
			g = &fileGroup{filePath: fp}
			groups[fp] = g
			order = append(order, fp)
		}
		if rr.Score > g.bestScore {
			g.bestScore = rr.Score
		}
		g.symbols = append(g.symbols, rr)
	}
	sort.Slice(order, func(i, j int) bool { return groups[order[i]].bestScore > groups[order[j]].bestScore })
	for _, fp := range order {
		g := groups[fp]
		fmt.Fprintf(sb, "### %s (%d symbols)\n", fp, len(g.symbols))
		for _, rr := range g.symbols {
			fmt.Fprintln(sb, FormatCodeSymbolLine(rr))
		}
		sb.WriteString("\n")
	}
}

// FormatCodePerQueryGroupByFile renders the group-by-file variant per query.
func FormatCodePerQueryGroupByFile(sb *strings.Builder, queries []string, perQuery [][]CodeResolvedResult) {
	if len(queries) == 1 {
		FormatCodeGroupByFile(sb, perQuery[0])
		return
	}
	for i, q := range queries {
		fmt.Fprintf(sb, "## Query %d: %q (%d results)\n\n", i+1, q, len(perQuery[i]))
		FormatCodeGroupByFile(sb, perQuery[i])
		if i < len(queries)-1 {
			sb.WriteString("\n")
		}
	}
}

// FormatCodeSymbolLine ports the server FormatCodeSymbolLine.
func FormatCodeSymbolLine(rr CodeResolvedResult) string {
	if !rr.Found {
		return fmt.Sprintf("- %s (score: %.4f)", rr.Node.Id, rr.Score)
	}
	n := rr.Node
	var line string
	if n.SymbolName != "" {
		line = fmt.Sprintf("- `%s` (%s, L%d-%d)", n.SymbolName, n.Type, n.StartLine, n.EndLine)
	} else {
		line = fmt.Sprintf("- L%d-%d (%s)", n.StartLine, n.EndLine, n.Type)
	}
	if n.Summary != "" {
		line += " — " + n.Summary
	}
	return line
}
