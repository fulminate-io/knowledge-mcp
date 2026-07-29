// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_code_search_multirepo.go carries the CROSS-REPO arm of the
// client-side code search. It is split out of intercept_query_code_search.go
// purely on file size (the repo enforces a 500-line cap via lefthook); the
// single-repo arm, the shared composer that dispatches between them, and the
// per-query search primitives all remain in that file. Nothing here is
// referenced from outside the package.
//
// PERF-SHAPE: the per-repo searches fan out in PARALLEL under a NumCPU-bounded
// goroutine pool, mirroring searchCodeMultiRepo's WaitGroup fan-out, and each
// repo's per-query lists stay SEPARATE (the engine's multi-query fusion would
// collapse them). Results merge by score across repos, then cap to limit.

// composeCodeSearchMultiRepo resolves the repo set then fans the per-repo
// searches in PARALLEL (NumCPU-bounded pool), merges by score, and renders the
// cross-repo shape.
func composeCodeSearchMultiRepo(ctx context.Context, deps ClientDeps, cdeps codeSearchDeps, a codeSearchArgs, queries []string, queryVecs [][]byte, limit int, includeSource, groupByFile bool) kgtools.ToolResult {
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
			pq := searchAllQueries(ctx, cdeps, target, queries, queryVecs, limit, a.PathPrefix, repo)
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

	if a.Format == "json" {
		return engine.RenderForCaller(strings.Join(queries, " "), flattenCodeResults(merged), "json", nil, "")
	}

	counts := make([]int, len(queries))
	for i := range merged {
		counts[i] = len(merged[i])
	}
	var sb strings.Builder
	sb.WriteString("Cross-repo search across " + strings.Join(repoNames, ", ") + "\n")
	engine.WriteCodePerQuerySearchHeader(&sb, queries, counts, codeSearchModeLabel(queryVecs))
	if groupByFile {
		engine.FormatCodePerQueryGroupByFile(&sb, queries, merged)
	} else {
		engine.FormatCodePerQueryCrossRepo(&sb, queries, merged, includeSource)
	}
	return textResult(sb.String())
}
