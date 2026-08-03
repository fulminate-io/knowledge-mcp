// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"

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
// Staleness is rendered as a footer (appendStalenessFooter → codeStalenessFooter
// in code_staleness.go): the collect path records the HEAD SHA + collection time
// onto code-graph metadata, surfaced via the GraphInfo catalog, so the footer
// shows real commits-behind + last-collected-when. Degrades to no footer when no
// metadata is recorded (graphs collected before staleness tracking landed).

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
	// Format selects the render shape: "json" emits the SearchJSONResponse
	// envelope (via engine.RenderForCaller), anything else stays on the text
	// path. Decoded by both interceptSearchCode and InterceptQueryCodeSearch.
	Format string `json:"format"`
	// QueryVector is the caller-supplied single query vector for this search's
	// query/queries set (a caller supplies at most one per call). The Go stdlib
	// JSON []byte codec base64-decodes it transparently, matching
	// maybeEmbedQuery's json.Marshal(vec) wire shape — no manual base64 step.
	// The PER-QUERY auto-embed slice (maybeEmbedCodeQueries) is built separately;
	// this caller vector is broadcast to queryVecs[0] during threading.
	QueryVector []byte `json:"query_vector"`
}

// InterceptQueryCodeSearch claims query(graph:code) with text/queries and no id
// (the search shape). Returns (false,_) otherwise (id → analyze; stats → code
// stats; non-code → other intercepts).
func InterceptQueryCodeSearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
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
	if err := accountQueryParams(armCodeSearch, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("code search: graph client unavailable")
	}

	queryVecs := buildCodeQueryVecs(maybeEmbedCodeQueries(ctx, codeEmbedder(deps), queries), a.QueryVector)
	return true, composeCodeSearch(ctx, deps, gc.Execute, a, queries, queryVecs)
}

// codeEmbedder returns deps.Embedder() or nil when deps is nil (the exec-seam
// unit tests pass nil deps with a fake Execute). A nil embedder degrades the
// search to BM25-only, so the nil-deps path stays valid.
func codeEmbedder(deps ClientDeps) embed.BinaryEmbedder {
	if deps == nil {
		return nil
	}
	return deps.Embedder()
}

// maybeEmbedCodeQueries embeds every query in ONE EmbedBinaryBatch round-trip
// and returns a [][]byte parallel to queries (queryVecs[i] embeds queries[i]).
// Returns nil for a nil embedder or empty queries, and nil on any embed error
// (the search degrades to BM25-only rather than failing). Mirrors the
// knowledge-arm maybeEmbedQuery idiom, batched for the code-search fan-out.
func maybeEmbedCodeQueries(ctx context.Context, emb embed.BinaryEmbedder, queries []string) [][]byte {
	if emb == nil || len(queries) == 0 {
		return nil
	}
	vecs, err := emb.EmbedBinaryBatch(ctx, queries)
	if err != nil || len(vecs) == 0 {
		return nil
	}
	return vecs
}

// buildCodeQueryVecs broadcasts a caller-supplied single query vector to index 0
// (the caller supplies at most one vector per search call), overriding any
// auto-embedded vector there. When no caller vector is supplied it returns the
// auto-embedded slice unchanged (possibly nil → BM25-only).
func buildCodeQueryVecs(autoEmbedded [][]byte, callerVec []byte) [][]byte {
	if len(callerVec) == 0 {
		return autoEmbedded
	}
	out := autoEmbedded
	if len(out) == 0 {
		out = make([][]byte, 1)
	}
	out[0] = callerVec
	return out
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
// queryVecs is PER-QUERY (queryVecs[i] is the vector for queries[i]) — either
// the caller-supplied vector broadcast to index 0 or the auto-embedded batch.
// It is nil/empty when no embedder is wired and no caller vector was supplied,
// in which case the search stays BM25-only.
func composeCodeSearch(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, a codeSearchArgs, queries []string, queryVecs [][]byte) kgtools.ToolResult {
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 10
	}
	includeSource := a.IncludeSource == nil || *a.IncludeSource
	groupByFile := a.GroupByFile != nil && *a.GroupByFile

	// Readiness gate (bind-first startup): the per-query code search dereferences the segment
	// Manager (cdeps.mgr.Search) with NO nil-check, including INSIDE per-repo and
	// per-query goroutines — a nil Manager there panics in a goroutine and crashes
	// the daemon. During the bind-first wiring window deps.SegmentManager() is an
	// untyped nil, so gate here — the single chokepoint both entry points
	// (interceptSearchCode and InterceptQueryCodeSearch) funnel through, upstream
	// of any goroutine fan-out — and return the uniform not-ready error rather than
	// letting a nil Manager reach a goroutine. (deps is nil only in the exec-seam
	// unit tests that call the sub-composers directly with a real cdeps; the
	// chokepoint is never reached with nil deps in production.)
	if deps != nil && !deps.PipelineReady() {
		return errorResult("code search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}

	// GO-LIVE: the per-repo code search runs against the CLIENT engine
	// (Manager.Search → RRF) + hydration. Both code entry points (the search-tool
	// arm interceptSearchCode AND the query-tool arm InterceptQueryCodeSearch)
	// funnel through here, so threading the engine seam at this single point
	// reroutes both. cdeps carries the engine seam + the hydration caller down to
	// the per-query site. The Manager is nil during the bind-first wiring window
	// (rejected by the PipelineReady gate above) AND on a permanent pipeline
	// degrade (PipelineReady()==true but wirePipelineRuntime built no Manager) —
	// the latter is caught by the nil-mgr guard below. There is no server-Execute
	// fallback (the comment that once claimed one described a path never coded).
	cdeps := codeSearchDeps{exec: exec}
	if deps != nil {
		cdeps.mgr = deps.SegmentManager()
		cdeps.gc = deps.GraphCaller()
	}
	// Permanent-degrade guard (bind-first startup): loud-error before any per-repo/per-query
	// goroutine dereferences a nil Manager (a goroutine nil-deref crashes the
	// daemon). deps==nil is the exec-seam unit-test path that drives the
	// sub-composers directly with a real cdeps, so it is exempt.
	if deps != nil && cdeps.mgr == nil {
		return errorResult("code search: client segment engine unavailable (LLM pipeline degraded at boot)")
	}

	if len(a.Repos) > 0 || a.Repo == "all" {
		return composeCodeSearchMultiRepo(ctx, deps, cdeps, a, queries, queryVecs, limit, includeSource, groupByFile)
	}
	return composeCodeSearchSingleRepo(ctx, deps, cdeps, a, queries, queryVecs, limit, includeSource, groupByFile)
}

// codeSearchDeps bundles the per-query search seam: the CLIENT engine Manager +
// hydration caller (GO-LIVE path). Threaded through the code-search fan-out so
// each per-query site reaches the client engine. mgr is non-nil by the time a
// codeSearchDeps is built in composeCodeSearch — the PipelineReady gate at that
// chokepoint rejects the bind-first wiring window, when SegmentManager() is nil.
// exec is NOT a search fallback; it carries only the staleness-footer Execute
// (appendStalenessFooter). There is no server RETURN_MODE_SEARCH fallback.
type codeSearchDeps struct {
	mgr  SegmentSearcher
	gc   GraphCaller
	exec engine.ExecuteFn
}

// codeSearchModeLabel returns "hybrid" when any per-query vector is threaded
// (the server fuses BM25 + vector for those queries) and "text" when none is —
// replacing the previously hardcoded "hybrid" label, which was accurate only
// once a vector is present.
func codeSearchModeLabel(queryVecs [][]byte) string {
	for _, v := range queryVecs {
		if len(v) > 0 {
			return "hybrid"
		}
	}
	return "text"
}

// queryVecAt returns the per-query vector for index i, or nil when the slice is
// shorter than i+1 (BM25-only for that query).
func queryVecAt(queryVecs [][]byte, i int) []byte {
	if i < len(queryVecs) {
		return queryVecs[i]
	}
	return nil
}

// appendStalenessFooter appends the code-index staleness footer for the
// searched repo PLUS a loud paused-pipeline line when the LLM pipeline is in
// circuit-break, to a rendered result. The paused line is surfaced
// UNCONDITIONALLY — even when no code-staleness metadata exists — because a
// paused pipeline means search results are silently going stale and the
// operator must see it regardless of any code footer. Degrades to the body
// unchanged when neither a staleness footer nor a paused state applies.
func appendStalenessFooter(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, repo, branch string, res kgtools.ToolResult) kgtools.ToolResult {
	if deps == nil {
		return res
	}
	footer := codeStalenessFooter(ctx, exec, deps.RootDir(), repo, branch)
	if paused := pipelinePausedFooter(deps); paused != "" {
		if footer == "" {
			footer = paused
		} else {
			footer += "\n" + paused
		}
	}
	if footer == "" {
		return res
	}
	for i := range res.Content {
		if res.Content[i].Type == "text" {
			res.Content[i].Text += "\n\n" + footer
			return res
		}
	}
	return res
}

// pipelinePausedFooter returns the loud paused-pipeline footer line(s) when one
// or both axes are latched paused (circuit-break or manual), or "" when both
// running, disabled, or the deps don't expose pipeline control (test fakes). The
// breakers are now PER-AXIS, so the footer NAMES which axis is paused (so an
// operator seeing this knows the OTHER axis's work is still flowing). Each line
// PRESERVES that axis's verbatim Reason — which NAMES the dominant error class of
// the failure window (with counts) — and only ADDS the axis label in front of it.
func pipelinePausedFooter(deps ClientDeps) string {
	pp, ok := deps.(pipelinePauser)
	if !ok {
		return ""
	}
	st, wired := pp.PipelineStatus()
	if !wired || !st.Paused {
		return ""
	}
	var lines []string
	if st.Summary.Paused {
		lines = append(lines, "summary axis PAUSED (circuit-break: "+st.Summary.Reason+")")
	}
	if st.Embed.Paused {
		lines = append(lines, "embed axis PAUSED (circuit-break: "+st.Embed.Reason+")")
	}
	if len(lines) == 0 { // aggregate Paused with neither flag set: should not happen
		lines = append(lines, "pipelines PAUSED (circuit-break: "+st.Reason+")")
	}
	return strings.Join(lines, "\n") + " — run manage(operation:\"resume_pipeline\") to re-enable."
}

// composeCodeSearchSingleRepo runs one RETURN_MODE_SEARCH Execute per query
// (parallel) against the single repo graph, then renders.
func composeCodeSearchSingleRepo(ctx context.Context, deps ClientDeps, cdeps codeSearchDeps, a codeSearchArgs, queries []string, queryVecs [][]byte, limit int, includeSource, groupByFile bool) kgtools.ToolResult {
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}
	// Pass a.Repo (not "") as the repo tag so each CodeResolvedResult.Repo carries
	// the request repo — flattenCodeResults stamps it as the json GraphInstance
	// Byte-for-byte safe for the single-repo TEXT path: only the
	// MULTI-repo FormatCodeCrossRepoFlatResults reads rr.Repo; the single-repo
	// formatters (FormatCodePerQueryResults / FormatCodePerQueryGroupByFile) never do.
	perQuery := searchAllQueries(ctx, cdeps, target, queries, queryVecs, limit, a.PathPrefix, a.Repo)
	perQuery = applyCodeResultFilters(perQuery, a)

	if a.Format == "json" {
		return engine.RenderForCaller(strings.Join(queries, " "), flattenCodeResults(perQuery), "json", nil, "")
	}

	counts := make([]int, len(perQuery))
	for i := range perQuery {
		counts[i] = len(perQuery[i])
	}
	var sb strings.Builder
	engine.WriteCodePerQuerySearchHeader(&sb, queries, counts, codeSearchModeLabel(queryVecs))
	if groupByFile {
		engine.FormatCodePerQueryGroupByFile(&sb, queries, perQuery)
	} else {
		engine.FormatCodePerQueryResults(&sb, queries, perQuery, includeSource)
	}
	res := textResult(engine.FormatCodeWithRepo(repoLabelFor(a.Repo, a.Branch), sb.String()))
	return appendStalenessFooter(ctx, deps, cdeps.exec, a.Repo, a.Branch, res)
}

// searchAllQueries runs one RETURN_MODE_SEARCH Execute per query (parallel,
// NumCPU-bounded), mirroring SearchOneGraph's per-query goroutine fan-out, and
// returns per-query resolved results (path_prefix filtered, repo-tagged).
func searchAllQueries(ctx context.Context, cdeps codeSearchDeps, target *knowledgev1.GraphSelector, queries []string, queryVecs [][]byte, limit int, pathPrefix, repo string) [][]engine.CodeResolvedResult {
	perQuery := make([][]engine.CodeResolvedResult, len(queries))
	if len(queries) == 1 {
		perQuery[0] = searchOneCodeQuery(ctx, cdeps, target, queries[0], queryVecAt(queryVecs, 0), limit, pathPrefix, repo)
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
			perQuery[idx] = searchOneCodeQuery(ctx, cdeps, target, query, queryVecAt(queryVecs, idx), limit, pathPrefix, repo)
		}(i, q)
	}
	wg.Wait()
	return perQuery
}

// searchOneCodeQuery resolves one query's hits into CodeResolvedResults
// (path_prefix filtered, repo-tagged). Runs against the CLIENT per-repo engine
// (Manager.Search → RRF) + RETURN_MODE_NODES hydration. The segment Manager is
// guaranteed non-nil here: composeCodeSearch gates on PipelineReady at its top
// (before cdeps.mgr is assigned), so the bind-first wiring window — when
// SegmentManager() is nil — never reaches this deref. There is no server
// RETURN_MODE_SEARCH fallback. An un-collected repo (no segments) yields zero hits.
func searchOneCodeQuery(ctx context.Context, cdeps codeSearchDeps, target *knowledgev1.GraphSelector, query string, queryVec []byte, limit int, pathPrefix, repo string) []engine.CodeResolvedResult {
	return searchOneCodeQueryClient(ctx, cdeps, target, query, queryVec, limit, pathPrefix, repo)
}

// searchOneCodeQueryClient runs one code query against the CLIENT engine and
// hydrates the ranked Hit IDs into CodeResolvedResults. The graph name for the
// code engine is the repo (segmentdist.graphSelector routes GraphCode's name to
// the Repo field). path_prefix is applied post-hydrate, matching the server arm.
// SEGMENT-POOL KEY SYMMETRY. The pipeline SHIPS segments under the
// OVERLAY-QUALIFIED graph name — worker_embed.go passes key.GraphName verbatim to
// the segment write entry points, and that name is "repo@branch" whenever the gap
// scan read from a branch overlay (the same reason pipeline/rpc.go splits it back
// apart on writeback). Reading under the BARE repo alone therefore opens a pool
// nothing has filled since the last default-branch collect, and re-collecting can
// never repair it: the coverage probe and heal factory key on the pipeline's name
// too, so they see that pool healthy and never inspect the one the reader opens.
//
// BOTH pools are required, not just the overlay. The bare pool holds the
// full-corpus segment written by the default-branch collect; the overlay pool
// accumulates per-collect deltas on top of it. Overlay-only would silently drop
// every file untouched since the branch was cut — the bulk of any repo.
//
// Dedup is by node ID with the OVERLAY winning, mirroring the composite
// base+overlay precedence that hydrateEngineHits applies one call later. Scores
// from the two pools are produced against different IDF corpora and so are not
// strictly comparable; the merge still orders on them, which is a RANKING
// approximation only — a node present in both pools always resolves to exactly
// one entry, so recall and identity are exact. A pool that errors or does not
// exist contributes nothing rather than failing the search, so a repo on its
// default branch (where no overlay pool was ever written) behaves as before.
// mergeOverlayOverBase unions two pools' hits with the OVERLAY winning any id
// present in both, then re-ranks and truncates to limit.
//
// Overlay-wins mirrors the composite base+overlay precedence hydrateEngineHits
// applies one call later, so a node changed on the branch resolves to the branch
// version rather than to two entries or to the stale base one.
//
// The re-rank is a RANKING APPROXIMATION and deliberately so: the two pools score
// against different IDF corpora, so their scores are not strictly comparable.
// Ordering across the union is therefore approximate — but dedup is by id, so
// recall and identity are exact, which is what correctness depends on.
func mergeOverlayOverBase(overlayHits, baseHits []searchengine.Hit, limit int) []searchengine.Hit {
	seen := make(map[string]struct{}, len(overlayHits))
	for _, h := range overlayHits {
		seen[h.ID] = struct{}{}
	}
	merged := overlayHits
	for _, h := range baseHits {
		if _, dup := seen[h.ID]; dup {
			continue
		}
		merged = append(merged, h)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func searchOneCodeQueryClient(ctx context.Context, cdeps codeSearchDeps, target *knowledgev1.GraphSelector, query string, queryVec []byte, limit int, pathPrefix, repo string) []engine.CodeResolvedResult {
	base := target.GetRepo()
	hits, err := cdeps.mgr.Search(ctx, kgtypes.GraphCode, base, query, queryVec, limit)
	if err != nil {
		hits = nil
	}
	if overlay := overlayName(base, target.GetBranch()); overlay != base {
		overlayHits, overlayErr := cdeps.mgr.Search(ctx, kgtypes.GraphCode, overlay, query, queryVec, limit)
		if overlayErr == nil && len(overlayHits) > 0 {
			hits = mergeOverlayOverBase(overlayHits, hits, limit)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sel := hydrateSelector{Graph: "code", Repo: target.GetRepo(), Branch: target.GetBranch()}
	results, err := hydrateEngineHits(ctx, cdeps.gc, sel, hits)
	if err != nil {
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
