// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// searchArgs is the compile-local view of the `search` tool's wire shape. It
// mirrors the server-side searchArgs (tools_search_args.go) for the fields the
// reducible path needs; code-graph-only fields (include_source, group_by_file,
// path_prefix, repos, staleness, include_tests, test_kinds, current_head,
// uncommitted_count, commits_behind) are intentionally omitted because a
// code-graph search is SPECIALIZED (ok=false before they ever matter).
type searchArgs struct {
	Query        string   `json:"query"`
	Queries      []string `json:"queries"`
	Graph        string   `json:"graph"`
	Name         string   `json:"name"`
	Language     string   `json:"language"`
	Account      string   `json:"account"`
	Repo         string   `json:"repo"`
	Branch       string   `json:"branch"`
	Limit        int      `json:"limit"`
	Offset       int      `json:"offset"`
	Types        []string `json:"types"`
	ResourceType string   `json:"resource_type"`
	QueryVector  string   `json:"query_vector"`
	Rerank       *bool    `json:"rerank"`

	// Format/Fields are render-only (Compile ignores them — the engine has no
	// format concern); Render reads them to pick text/json + projection.
	Format string   `json:"format"`
	Fields []string `json:"fields"`
}

// compileSearch translates a reducible `search` call (graph in
// knowledge/practice/cloud/cicd/linkage/web/pdf) into a QueryPlan QSearch.
// Returns ok=false (default-deny → legacy) for:
//   - graph=code (SPECIALIZED: HandleSearchCode)
//   - an empty query set (nothing to search)
//
// Multi-type and resource_type-filtered cloud/cicd searches ARE reducible:
// multi-type rides the node_types carrier (the engine post-filters + trims).
// resource_type is CLIENT POST-FILTERED on the rendered result set:
// an OP_PREFIX metadata predicate does NOT compose with a
// QSearch post-rank (nodeMatchesMetaPredicate runs only at index-Match time, not
// in the search compositor — see TestExecute_MetaPredicate_Prefix_SearchNoCompose),
// so emitting an OP_PREFIX predicate into a search plan would silently drop the
// filter. The compiled plan therefore carries NO resource_type signal; the
// render path trims the returned SearchList by the resource_type prefix instead
// (filterByResourceTypePrefix, render_search.go — behavior-identical to the
// legacy server postFilterResourceType / FilterCloudResultsByResourceType).
//
// The multi-query slice rides as ONE repeated Queries field — the engine owns
// score-sum fusion, NOT a client-side N-fanout merge. NO
// normalization here (limit defaults, node-type canon, dedup all live in the
// engine).
func compileSearch(args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, false
	}

	if isCodeGraph(a.Graph) {
		return nil, false // SPECIALIZED: code search stays on HandleSearchCode.
	}

	queries := mergeQueries(a.Query, a.Queries)
	if len(queries) == 0 {
		return nil, false // no query to run.
	}

	plan := &knowledgev1.QueryPlan{
		Queries:    queries,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
	}

	// node_types is the SEARCH plural type-filter — the engine post-filters +
	// trims the ranked set to these types (one OR many). A single-type or
	// multi-type search both ride the full set in the node_types carrier; the
	// engine canonicalizes the tokens and owns the post-filter.
	if len(a.Types) >= 1 {
		plan.Selection = &knowledgev1.Selection{NodeTypes: a.Types}
	}

	// resource_type does NOT ride the plan: an OP_PREFIX predicate is inert on a
	// QSearch (TestExecute_MetaPredicate_Prefix_SearchNoCompose), so the
	// client post-filters the returned SearchList by the resource_type prefix in
	// the render path (renderSearchTool → filterByResourceTypePrefix). Routing
	// stays reducible — only the FILTER moved from engine post-rank to client
	// post-render.

	// query_vector → one QueryVecs entry (the engine validates the 32-byte
	// length and returns CodeInvalidArgument on a mismatch — no client-side
	// length check, no normalization).
	if a.QueryVector != "" {
		raw, err := base64Decode(a.QueryVector)
		if err != nil {
			return nil, false // malformed base64 → fall through to legacy's validation.
		}
		plan.QueryVecs = [][]byte{raw}
	}

	// rerank tri-state: only set when the caller supplied it (nil = engine
	// default). proto3 optional bool preserves the nil/true/false the store
	// getter documents.
	if a.Rerank != nil {
		plan.Rerank = a.Rerank
	}

	// Limit/Offset ride ONLY when the caller supplied them — the engine owns
	// the unified default limit, so injecting a default here would be
	// client-side normalization (out of scope).
	if a.Limit > 0 {
		plan.Limit = int32(a.Limit)
	}
	if a.Offset > 0 {
		plan.Offset = int32(a.Offset)
	}

	req := &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch),
	}
	return req, true
}

// mergeQueries combines a single query and a batch queries slice into one
// ordered, deduplicated list — mirroring the server-side mergeQueries
// (tools_search_source.go:69). This is NOT result-set dedup (the engine owns
// that); it is the trivial input-list normalization the legacy tool also does
// before building the search, so the compiled Queries match the legacy input.
func mergeQueries(query string, queries []string) []string {
	var result []string
	seen := make(map[string]bool)
	if query != "" {
		result = append(result, query)
		seen[query] = true
	}
	for _, q := range queries {
		if q != "" && !seen[q] {
			result = append(result, q)
			seen[q] = true
		}
	}
	return result
}
