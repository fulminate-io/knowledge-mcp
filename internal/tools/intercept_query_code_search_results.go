// SPDX-License-Identifier: Apache-2.0

package tools

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_code_search_results.go holds the LINKAGE-independent result
// helpers for the code-search arm (intercept_query_code_search.go): the flatten
// step that feeds the format:"json" envelope and the default comment-exclusion +
// test-flag filters. Split out verbatim to keep the parent file under the
// file-length limit — a pure move, no behavior change.

// excludedCodeSearchTypes are the low-signal node types dropped by default (port
// of the server excludedCodeTypes).
var excludedCodeSearchTypes = map[string]bool{
	"comment":             true,
	"block_mapping_pair":  true,
	"block_sequence_item": true,
}

// flattenCodeResults concatenates the per-query resolved code results into a
// single flat []engine.SearchResult{Node,Score} for the format:"json" arm. The
// per-query grouping is a text-render concern (the markdown header tallies hits
// per query); the JSON envelope is a flat result list mirroring renderJSON's
// shape, so the groups are concatenated in query order.
//
// Each row is stamped with its SOURCE-GRAPH identity: Graph:"code" and
// GraphInstance:r.Repo, so the graph-UI traverses each code result in its own
// repo. r.Repo is set per-repo by searchAllQueries — for MULTI-repo the instance
// VARIES across results (each result carries its own repo); for SINGLE-repo it is
// the request repo (the :311 a.Repo fix makes it non-empty). The CodeResolvedResult
// boundary dropped the SearchResult.Graph/GraphInstance the hydrate funnel stamped,
// so this is the re-stamp on the way into the json envelope.
func flattenCodeResults(perQuery [][]engine.CodeResolvedResult) []engine.SearchResult {
	var out []engine.SearchResult
	for _, results := range perQuery {
		for _, r := range results {
			out = append(out, engine.SearchResult{
				Node:          r.Node,
				Score:         r.Score,
				Graph:         "code",
				GraphInstance: r.Repo,
			})
		}
	}
	return out
}

// applyCodeResultFilters applies the default comment-exclusion + the test-flag
// filter (port of excludeCommentResults + filterCodeResultsByTestFlag).
func applyCodeResultFilters(perQuery [][]engine.CodeResolvedResult, a codeSearchArgs) [][]engine.CodeResolvedResult {
	includeComments := a.IncludeComments != nil && *a.IncludeComments
	kinds := make(map[string]bool, len(a.TestKinds))
	for _, k := range a.TestKinds {
		kinds[k] = true
	}
	filterTests := a.IncludeTests != nil || len(a.TestKinds) > 0
	out := make([][]engine.CodeResolvedResult, len(perQuery))
	for i, results := range perQuery {
		kept := make([]engine.CodeResolvedResult, 0, len(results))
		for _, r := range results {
			if !includeComments && excludedCodeSearchTypes[r.Node.Type] {
				continue
			}
			if filterTests && !keepCodeTestResult(r.Node, a.IncludeTests, kinds) {
				continue
			}
			kept = append(kept, r)
		}
		out[i] = kept
	}
	return out
}

// keepCodeTestResult ports the server keepResult test-flag predicate.
func keepCodeTestResult(n *knowledgev1.Node, includeTests *bool, kinds map[string]bool) bool {
	if !n.IsTest {
		return true
	}
	if includeTests != nil && !*includeTests {
		return false
	}
	if len(kinds) == 0 {
		return true
	}
	return kinds[n.TestKind]
}
