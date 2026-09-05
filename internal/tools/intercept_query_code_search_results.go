// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"slices"

	"google.golang.org/protobuf/proto"

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
//
// includeSource is the caller's include_source, and this is where the json branch
// honors it: the renderer copies Content and Source unconditionally and
// RenderForCaller takes no such flag, so the suppression has to happen on the way
// IN. The text path's own gate lives in the formatter, which is why the two paths
// read the same flag at different sites rather than sharing one.
func flattenCodeResults(perQuery [][]engine.CodeResolvedResult, includeSource bool) []engine.SearchResult {
	var out []engine.SearchResult
	for _, results := range perQuery {
		for _, r := range results {
			out = append(out, engine.SearchResult{
				Node:          bodyForRender(r.Node, includeSource),
				Score:         r.Score,
				Graph:         "code",
				GraphInstance: r.Repo,
			})
		}
	}
	return out
}

// bodyForRender returns the node to render: n itself when the caller wants the
// source, and a CLONE with both body carriers cleared when it does not.
//
// THE CLONE IS THE POINT. CodeResolvedResult holds a *knowledgev1.Node that came
// straight out of the hydrate, and the search result copies that pointer — so
// clearing the body in place would blank the node for every other holder of the
// same pointer, turning a presentation choice into a data mutation. Cloning is
// also why this cannot live in the renderer, which is handed the results and not
// the flag. It goes through proto.CloneOf rather than a struct copy, for the
// reason nodeWithParentHeading records: a generated message carries internal
// state that must not be copied by value, and `go vet` says so.
//
// BOTH carriers are cleared, not just Content. `content` and `source` are
// separately persisted node fields (proto Node: content = 8, source = 12), and
// include_source promises no source text in the row rather than in one key of it;
// code symbol nodes populate only Content today, so clearing Source alone would
// have been a no-op that looked like a fix.
func bodyForRender(n *knowledgev1.Node, includeSource bool) *knowledgev1.Node {
	if includeSource || n == nil {
		return n
	}
	stripped := proto.CloneOf(n)
	stripped.Content = ""
	stripped.Source = ""
	return stripped
}

// projectionNamesBody reports whether a caller's json projection names the key
// that carries the node body. It backs composeCodeSearch's contradictory-input
// refusal: naming `content` while passing include_source:false asks for the body
// and for no body in one call.
//
// Only `content` counts, and that is the ticket's ruling rather than an
// oversight: `source` is a separate node field that code symbols never populate,
// so a projection naming it returns an empty string under either flag and states
// nothing contradictory.
func projectionNamesBody(fields []string) bool {
	return slices.Contains(fields, "content")
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
