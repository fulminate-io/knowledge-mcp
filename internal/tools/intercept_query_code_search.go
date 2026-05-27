// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_code_search.go is the client-side claim for query(graph:code)
// with text/queries (the server routeCodeSearch → HandleSearchCode shape). The
// codegraph search relocates client-side: it composes a DEDICATED code-search
// RETURN_MODE_SEARCH QueryPlan (NOT compileSearch, which default-denies code) +
// gc.Execute, then does the code-specific group_by_file / path_prefix /
// include_source / include_comments / test-kind presentation client-side.
//
// PERF-SHAPE: multi-repo fans the per-repo searches in PARALLEL (a NumCPU-bounded
// goroutine pool, mirroring searchCodeMultiRepo's WaitGroup fan-out + the
// per-query goroutines in SearchOneGraph), then merges by score
// (mergeMultiRepoResults). Per-query lists stay SEPARATE (the engine's
// multi-query fusion would collapse them) — one Execute per (repo,query).
//
// Staleness ("Indexed N ago") is NOT reconstructable client-side (no graph-meta
// carrier — finding e71f53bb); it degrades to empty (matching StalenessInfoWith's
// own degrade-on-missing-meta).

// excludedCodeSearchTypes are the low-signal node types dropped by default (port
// of the server excludedCodeTypes).
var excludedCodeSearchTypes = map[string]bool{
	"comment":             true,
	"block_mapping_pair":  true,
	"block_sequence_item": true,
}

// codeSearchArgs is the code-search view of the query args.
type codeSearchArgs struct {
	Graph           string   `json:"graph"`
	ID              string   `json:"id"`
	Text            string   `json:"text"`
	Queries         []string `json:"queries"`
	Repo            string   `json:"repo"`
	Repos           []string `json:"repos"`
	Branch          string   `json:"branch"`
	Mode            string   `json:"mode"`
	Limit           flexInt  `json:"limit"`
	PathPrefix      string   `json:"path_prefix"`
	GroupByFile     *bool    `json:"group_by_file"`
	IncludeSource   *bool    `json:"include_source"`
	IncludeComments *bool    `json:"include_comments"`
	IncludeTests    *bool    `json:"include_tests"`
	TestKinds       []string `json:"test_kinds"`
}

// InterceptQueryCodeSearch claims query(graph:code) with text/queries and no id
// (the search shape). Returns (false,_) otherwise (id → analyze; stats → code
// stats; non-code → other intercepts).
func InterceptQueryCodeSearch(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a codeSearchArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph != "code" || a.ID != "" || a.Mode == "stats" {
		return false, kgtools.ToolResult{}
	}
	queries := mergeCodeQueries(a.Text, a.Queries)
	if len(queries) == 0 {
		return false, kgtools.ToolResult{} // no query → not the search shape.
	}
	gc := deps.GraphClient()
	if gc == nil {
		return true, errorResult("code search: graph client unavailable")
	}
	return true, composeCodeSearch(context.Background(), deps, gc.Execute, a, queries)
}

func mergeCodeQueries(query string, queries []string) []string {
	var out []string
	if query != "" {
		out = append(out, query)
	}
	out = append(out, queries...)
	return out
}

// composeCodeSearch dispatches single-repo vs multi-repo (repos[] / repo=all).
func composeCodeSearch(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, a codeSearchArgs, queries []string) kgtools.ToolResult {
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 10
	}
	includeSource := a.IncludeSource == nil || *a.IncludeSource
	groupByFile := a.GroupByFile != nil && *a.GroupByFile

	if len(a.Repos) > 0 || a.Repo == "all" {
		return composeCodeSearchMultiRepo(ctx, deps, exec, a, queries, limit, includeSource, groupByFile)
	}
	return composeCodeSearchSingleRepo(ctx, exec, a, queries, limit, includeSource, groupByFile)
}

// composeCodeSearchSingleRepo runs one RETURN_MODE_SEARCH Execute per query
// (parallel) against the single repo graph, then renders.
func composeCodeSearchSingleRepo(ctx context.Context, exec engine.ExecuteFn, a codeSearchArgs, queries []string, limit int, includeSource, groupByFile bool) kgtools.ToolResult {
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}
	perQuery := searchAllQueries(ctx, exec, target, queries, limit, a.PathPrefix, "")
	perQuery = applyCodeResultFilters(perQuery, a)

	counts := make([]int, len(perQuery))
	for i := range perQuery {
		counts[i] = len(perQuery[i])
	}
	var sb strings.Builder
	engine.WriteCodePerQuerySearchHeader(&sb, queries, counts, "hybrid")
	if groupByFile {
		engine.FormatCodePerQueryGroupByFile(&sb, queries, perQuery)
	} else {
		engine.FormatCodePerQueryResults(&sb, queries, perQuery, includeSource)
	}
	return textResult(engine.FormatCodeWithRepo(repoLabelFor(a.Repo, a.Branch), sb.String()))
}

// composeCodeSearchMultiRepo resolves the repo set then fans the per-repo
// searches in PARALLEL (NumCPU-bounded pool), merges by score, and renders the
// cross-repo shape.
func composeCodeSearchMultiRepo(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, a codeSearchArgs, queries []string, limit int, includeSource, groupByFile bool) kgtools.ToolResult {
	repos := a.Repos
	if len(repos) == 0 { // repo=all → enumerate loaded code graphs.
		names, err := listGraphNamesOfType(ctx, deps, "code")
		if err != nil {
			return errorResult("resolve repos: " + err.Error())
		}
		repos = names
	}
	if len(repos) == 0 {
		return textResult("No code graphs found.")
	}

	type repoResult struct {
		repo     string
		perQuery [][]engine.CodeResolvedResult
	}
	var (
		mu  sync.Mutex
		all []repoResult
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for _, repo := range repos {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			target := &knowledgev1.GraphSelector{Graph: "code", Repo: repo}
			pq := searchAllQueries(ctx, exec, target, queries, limit, a.PathPrefix, repo)
			mu.Lock()
			all = append(all, repoResult{repo: repo, perQuery: pq})
			mu.Unlock()
		}(repo)
	}
	wg.Wait()

	// Merge per-query across repos, sort by score desc, cap to limit
	// (mergeMultiRepoResults shape).
	merged := make([][]engine.CodeResolvedResult, len(queries))
	repoNames := make([]string, 0, len(all))
	for _, rr := range all {
		repoNames = append(repoNames, rr.repo)
		for i := range queries {
			if i < len(rr.perQuery) {
				merged[i] = append(merged[i], rr.perQuery[i]...)
			}
		}
	}
	sort.Strings(repoNames)
	for i := range merged {
		sort.Slice(merged[i], func(x, y int) bool { return merged[i][x].Score > merged[i][y].Score })
		if len(merged[i]) > limit {
			merged[i] = merged[i][:limit]
		}
	}
	merged = applyCodeResultFilters(merged, a)

	counts := make([]int, len(queries))
	for i := range merged {
		counts[i] = len(merged[i])
	}
	var sb strings.Builder
	sb.WriteString("Cross-repo search across " + strings.Join(repoNames, ", ") + "\n")
	engine.WriteCodePerQuerySearchHeader(&sb, queries, counts, "hybrid")
	if groupByFile {
		engine.FormatCodePerQueryGroupByFile(&sb, queries, merged)
	} else {
		engine.FormatCodePerQueryCrossRepo(&sb, queries, merged, includeSource)
	}
	return textResult(sb.String())
}

// searchAllQueries runs one RETURN_MODE_SEARCH Execute per query (parallel,
// NumCPU-bounded), mirroring SearchOneGraph's per-query goroutine fan-out, and
// returns per-query resolved results (path_prefix filtered, repo-tagged).
func searchAllQueries(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, queries []string, limit int, pathPrefix, repo string) [][]engine.CodeResolvedResult {
	perQuery := make([][]engine.CodeResolvedResult, len(queries))
	if len(queries) == 1 {
		perQuery[0] = searchOneCodeQuery(ctx, exec, target, queries[0], limit, pathPrefix, repo)
		return perQuery
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for i, q := range queries {
		wg.Add(1)
		go func(idx int, query string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			perQuery[idx] = searchOneCodeQuery(ctx, exec, target, query, limit, pathPrefix, repo)
		}(i, q)
	}
	wg.Wait()
	return perQuery
}

// searchOneCodeQuery issues the dedicated code-search RETURN_MODE_SEARCH Execute
// for one query and resolves the hits into CodeResolvedResults (path_prefix
// filtered). Port of resolveSearchResults over the generic search carrier.
func searchOneCodeQuery(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, query string, limit int, pathPrefix, repo string) []engine.CodeResolvedResult {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Queries:    []string{query},
			Limit:      int32(limit),
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
		}},
		Target: target,
	})
	if err != nil {
		return nil
	}
	results, derr := engine.DecodeSearch(resp)
	if derr != nil {
		return nil
	}
	out := make([]engine.CodeResolvedResult, 0, len(results))
	for _, hr := range results {
		if pathPrefix != "" && !strings.HasPrefix(hr.Node.FilePath, pathPrefix) {
			continue
		}
		out = append(out, engine.CodeResolvedResult{Score: hr.Score, Node: hr.Node, Found: true, Repo: repo})
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
