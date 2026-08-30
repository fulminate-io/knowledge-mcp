// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_practice_linkage.go is the client-side claim for the practice
// and linkage per-graph query shapes the server routePracticeQuery /
// routeLinkageTarget served (cmd/knowledge-server/tools/tools_query_practice.go,
// tools_query.go, tools_query_linkage.go), plus the web/pdf and checks arms that
// joined it later.
//
// practice shapes:
//   - list-graphs   (no language): enumerate practice graphs
//     (RETURN_MODE_GRAPH_NAMES Execute + Stats RPC counts).
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Practice Graph: <lang>").
//   - browse        (language, no text): type/types/status/meta filters + paging
//     lowered onto ONE Selection → practiceBrowse (intercept_query_practice_browse.go).
//   - search        (text + language): generic search Execute → RenderPracticeResults.
//   - metadata_stats / by-id: NOT served here — practiceShapeIsForeign declines
//     them so the intercepts that do serve them get the call.
//
// linkage shapes:
//   - list-graphs   (no id/text/mode): enumerate linkage graphs + topology hint.
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Linkage Graph") +
//     the proxy-by-foreign_graph breakdown.
//   - id getNode    : Execute ByID → node render.
//   - ranked text search: RETIRED → rankedSearchRetiredResult("linkage").
//
// web/pdf shapes:
//   - ranked text search: served CLIENT-SIDE by composeRawGraphSearch
//     (intercept_query_webpdf.go) — the raw graph is drained, ranked in memory
//     with BM25 and rendered with heading context. No vector arm exists for
//     these graphs, and the render says so.
//   - mode=stats    : CLAIMED here → one Stats RPC → RenderStatsBreakdown.
//   - every remaining index-free op (by-id getNode, type-browse, mode=modules)
//     passes through unhandled to the engineDispatch path (compileQuery lowers
//     them to ById/Match/GRAPH_NAMES — no RETURN_MODE_SEARCH).
//
// checks shapes:
//   - ranked text search: served CLIENT-SIDE through the segment engine
//     (intercept_checks_search.go). The graph is a SINGLETON, so a non-empty
//     instance name is REFUSED rather than ignored.
//   - metadata_stats / by-id / browse: NOT served here — checksShapeIsForeign
//     declines them, so the browse a caller inventories the corpus with keeps
//     working exactly as it does today.

// InterceptQueryPracticeLinkage claims query(graph in {practice, linkage, web, pdf, checks}).
func InterceptQueryPracticeLinkage(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch a.Graph {
	case "practice":
		// A PLAIN by-id read with NO language is claimed here ONLY to refuse it
		// legibly: it names no graph, so neither this arm nor the server resolver
		// can serve it. See practiceByIDNeedsLanguage.
		//
		// THE a.Mode == "" CONJUNCT IS LOAD-BEARING and not defensive padding. A
		// mode carries the payload to an arm that owns it and refuses it BY NAME —
		// mode:"examine" on a practice graph is refused by the examine arm naming
		// the graph and the surface examine does serve, which is a better message
		// than this one. Claiming every id-bearing practice payload stole those
		// shapes; the bootstrap parity suite caught it.
		if a.Language == "" && a.Mode == "" && (a.ID != "" || len(a.IDs) > 0) {
			return true, errorResult(practiceByIDNeedsLanguage)
		}
		if practiceShapeIsForeign(a) {
			return false, kgtools.ToolResult{} // metadata_stats / by-id -> the intercepts that already serve them.
		}
		sc, res, ok := statsSeamFor(deps, "practice")
		if !ok {
			return true, res
		}
		return true, routePracticeClient(ctx, deps, sc, a, params.Arguments)
	case "linkage":
		sc, res, ok := statsSeamFor(deps, "linkage")
		if !ok {
			return true, res
		}
		return true, routeLinkageClient(ctx, sc, a, params.Arguments)
	case "checks":
		// The checks graph's ranked text search, served through the client segment
		// engine. Every non-search shape declines and keeps its own path — the
		// browse in particular, which is what a caller reaches for to inventory the
		// corpus.
		return routeChecksQueryClient(ctx, deps, a)
	case "web", "pdf":
		// The ranked read is composed client-side over the drained raw graph, so
		// this arm DOES need the graph client.
		return routeWebPDFClient(ctx, deps, a, params.Arguments)
	default:
		return false, kgtools.ToolResult{}
	}
}

// practiceShapeIsForeign names the practice payload shapes this entry point does
// NOT serve, so the chain hands them to the intercept that does:
// mode=metadata_stats to InterceptQueryMetadataStats (dream.go dispatches it
// immediately after this one), and the two by-id shapes to the engineDispatch
// path. Without the decline every one of them fell into the ranked-search arm and
// came back as a CLEAN render of a different operation — the most misleading of
// the three routing failures, because nothing about the response marks it wrong.
// The (false, ToolResult{}) idiom is routeWebPDFClient's, applied to the shapes
// the practice arm must not claim.
func practiceShapeIsForeign(a queryArgs) bool {
	return a.Mode == "metadata_stats" || a.ID != "" || len(a.IDs) > 0
}

// statsSeamFor resolves the statsRPC seam for the practice/linkage arms, returning
// a legible error result (ok=false) when the graph client or stats seam is absent.
func statsSeamFor(deps ClientDeps, graph string) (statsRPC, kgtools.ToolResult, bool) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, errorResult(graph + ": graph client unavailable"), false
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return nil, errorResult(graph + ": stats seam unavailable"), false
	}
	return sc, kgtools.ToolResult{}, true
}

// routePracticeClient dispatches the four practice shapes. raw is the caller's
// verbatim payload, threaded explicitly (rather than stashed on queryArgs) so
// the per-arm accounting gate cannot be forgotten at a claim point.
func routePracticeClient(ctx context.Context, deps ClientDeps, gc statsRPC, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	// (1) No language → list practice graphs.
	if a.Language == "" {
		if err := accountQueryParams(armPracticeListGraphs, raw); err != nil {
			return errorResult(err.Error())
		}
		return listPracticeGraphs(ctx, deps)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		if err := accountQueryParams(armPracticeStats, raw); err != nil {
			return errorResult(err.Error())
		}
		return practiceStatsResult(ctx, gc, a)
	}
	// (2b) No ranked text, and not the "all" fan-out sentinel → BROWSE this
	// practice graph. BOTH conjuncts are load-bearing: the empty text selects the
	// browse shape, and the a.Language != "all" guard preserves the scatter-gather
	// sentinel below — without it a text-less language:"all" would browse a
	// practice graph literally named "all" and turn a working fan-out into an
	// empty or erroring browse.
	if practiceQueryText(a) == "" && a.Language != "all" {
		if err := accountQueryParams(armPracticeBrowse, raw); err != nil {
			return errorResult(err.Error())
		}
		return practiceBrowse(ctx, gc.Execute, a)
	}

	// (3) search with language.
	query := practiceQueryText(a)

	// (3a) language:"all" → scatter-gather fan-out across every loaded practice
	// graph (kills the silent-0). The empty-language list-graphs BROWSE above is
	// preserved; only the explicit "all" sentinel fans out.
	if a.Language == "all" {
		if err := accountQueryParams(armPracticeSearchFanOut, raw); err != nil {
			return errorResult(err.Error())
		}
		// REFUSED BEFORE THE FAN-OUT: an empty-text ranked search cannot return
		// anything, so running it costs a scatter-gather across every loaded
		// practice graph to produce a vacuous zero. Refusing here issues no read,
		// no embed and no wire call.
		if query == "" {
			return errorResult(practiceFanOutNeedsText)
		}
		return composePracticeSearchFanOut(ctx, deps, deps.SegmentManager(), query, a.Format, int(a.Limit), a.Fields)
	}

	// (3b) Route a specific-language practice search through the per-language CLIENT
	// engine (Manager.Search → RRF) + hydration. The segment Manager is wired for
	// the life of the daemon EXCEPT during the bind-first wiring window (bind-first startup),
	// which composePracticeSearchClient gates on PipelineReady at its top — so
	// there is no server RETURN_MODE_SEARCH fallback. list-graphs (arm 1) +
	// stats/sample shapes (arm 2) are unchanged — only the ranked search arm
	// reroutes. A graph whose segments are absent returns a loud segment-gap error
	// naming the rebuild remedy.
	if err := accountQueryParams(armPracticeSearch, raw); err != nil {
		return errorResult(err.Error())
	}
	return composePracticeSearchClient(ctx, deps, deps.SegmentManager(), a.Language, query, a.Format, int(a.Limit), a.Fields)
}

// composePracticeSearchClient runs the practice ranked-search arm against the
// CLIENT per-language engine: embed the query client-side (so the HNSW arm is
// exercised), Manager.Search(GraphPractice, language, …) → RRF, then ONE
// RETURN_MODE_NODES hydrate, rendered via the same RenderPracticeResults shape
// as the server arm. A nil embedder degrades to the BM25 arm; a graph whose
// segments are absent returns a loud segment-gap error naming the rebuild remedy.
//
// limit is the caller's row cap, resolved against knowledgeSearchDefaultLimit the
// same way composeKnowledgeSearch resolves its own, so an absent limit preserves
// the previous behavior exactly. fields is the caller's json projection, threaded
// into RenderForCaller where a literal nil used to sit.
func composePracticeSearchClient(
	ctx context.Context, deps ClientDeps, mgr SegmentSearcher,
	language, query, format string, limit int, fields []string,
) kgtools.ToolResult {
	// Readiness gate (bind-first startup): mgr.Search below dereferences the segment Manager
	// with no nil-check; during the bind-first wiring window SegmentManager() is an
	// untyped nil → panic. Gate before the deref. Both entry points (specific
	// language at practice_linkage.go and the search-tool arm) funnel through here.
	if !deps.PipelineReady() {
		return errorResult("practice search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	// Permanent-degrade guard (bind-first startup): PipelineReady()==true but a nil Manager
	// when wirePipelineRuntime degraded at boot — loud-error instead of a nil-Search
	// panic. No server RETURN_MODE_SEARCH fallback exists.
	if mgr == nil {
		return errorResult("practice search: client segment engine unavailable (LLM pipeline degraded at boot)")
	}
	// The embed error is CAPTURED rather than discarded: a failed embed degrades
	// the search to the BM25 arm alone, and on an empty result set that is the
	// difference between "nothing matched" and "the semantic arm never ran".
	queryVec, embErr := embedQueryForArm(ctx, deps, query)
	k := limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	hits, err := mgr.Search(ctx, kgtypes.GraphPractice, language, query, queryVec, k)
	if err != nil {
		return errorResult("practice search: client engine: " + err.Error())
	}
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), hydrateSelector{Graph: "practice", Language: language}, hits)
	if err != nil {
		return errorResult("practice search: hydrate: " + err.Error())
	}
	// The arm disclosure is computed from what ACTUALLY reached the engine, not from
	// what was asked for: queryVec is nil both when no embedder is configured and
	// when EmbedBinary failed, and either way the search ran BM25-only.
	modeLabel := segmentSearchModeLabel(query != "", len(queryVec) > 0)
	if len(results) == 0 {
		notice, loud := practiceZeroHitNotice(ctx, deps, language, embErr)
		if loud {
			return errorResult(notice)
		}
		if notice != "" && format != "json" {
			return appendNotice(engine.RenderPracticeResults(language, query, results, modeLabel), notice)
		}
	}
	if format == "json" {
		return engine.RenderForCaller(query, results, "json", fields, modeLabel)
	}
	return engine.RenderPracticeResults(language, query, results, modeLabel)
}

// composePracticeSearchFanOut is the scatter-gather practice search across ALL
// loaded practice graphs — the prong that kills the language:"all" silent-0.
// It mirrors composeCodeSearchMultiRepo (intercept_query_code_search.go): the
// graph set is enumerated DYNAMICALLY via listGraphNamesOfType (no hardcoded
// language list), the query is embedded EXACTLY ONCE up front and the vector
// reused for every per-graph Search (no N-embed fan-out), each graph is searched
// in PARALLEL under a NumCPU-bounded pool, then per-graph hits are score-merged
// (sorted desc, capped at the caller's resolved row limit) and rendered with
// per-graph attribution via RenderPracticeFanOut. An empty practice-graph set
// renders a clean "no graphs" result rather than a silent zero.
//
// limit is the caller's row cap, resolved ONCE before the fan-out (never inside
// the loop) and applied at BOTH ends: as the per-graph Search k and as the merge
// cap. Applying it at only one end would give a caller who asked for 25 exactly
// 25 per graph and then silently 10 back. fields is the caller's json projection.
func composePracticeSearchFanOut(
	ctx context.Context, deps ClientDeps, mgr SegmentSearcher,
	query, format string, limit int, fields []string,
) kgtools.ToolResult {
	// Readiness gate (bind-first startup): the per-graph mgr.Search runs INSIDE a goroutine
	// fan-out and dereferences the segment Manager with no nil-check — a nil
	// Manager there panics in a goroutine and crashes the daemon. During the
	// bind-first wiring window SegmentManager() is an untyped nil; gate before any
	// goroutine is launched. Both entry points (language:"all" at
	// practice_linkage.go and the search-tool practice arm) funnel through here.
	if !deps.PipelineReady() {
		return errorResult("practice search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	// Permanent-degrade guard (bind-first startup): PipelineReady()==true but a nil Manager
	// when wirePipelineRuntime degraded at boot — loud-error before any goroutine
	// fan-out dereferences it. No server RETURN_MODE_SEARCH fallback exists.
	if mgr == nil {
		return errorResult("practice search: client segment engine unavailable (LLM pipeline degraded at boot)")
	}
	names, err := listGraphNamesOfType(ctx, deps, "practice")
	if err != nil {
		return errorResult("practice fan-out: resolve graphs: " + err.Error())
	}
	if len(names) == 0 {
		return textResult("No practice graphs found.")
	}
	// Resolve the row cap ONCE, outside the fan-out: the goroutines below share it
	// as their per-graph Search k, and the merge cap after wg.Wait reuses the same
	// number.
	k := limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}

	// Embed the query a SINGLE time up front; the vector is reused for every
	// per-graph Search so the HNSW arm is exercised without an N-embed fan-out.
	//
	// The embed error is CAPTURED, exactly as its sibling composer captures it. This
	// lane previously shadowed embErr inside the if-statement and dropped it, so a
	// failed embed degraded EVERY practice graph in the fan-out to the BM25 arm with
	// no signal at all — the widest-blast-radius instance of the discard, and the
	// one least visible because the caller sees eight graphs' worth of results.
	queryVec, embErr := embedQueryForArm(ctx, deps, query)

	// THREE buckets, because a per-graph outcome has three meanings and collapsing
	// any two of them is how this fan-out lies.
	//   all      — the graph was searched and matched.
	//   failed   — the graph could not be searched (a non-nil error).
	//   unindexed — the graph returned zero hits with a NIL error AND has no ranked
	//               index. Indistinguishable at the Search seam from a genuine
	//               no-match, which is exactly why it needs its own probe.
	//
	// THE THIRD BUCKET IS THE ONE THAT WAS MISSING, and its absence was the quiet
	// half of the same lie the `failed` bucket fixes. A zero-segment graph answers
	// (nil, nil), so it was neither a result nor a failure — and because the
	// per-graph gap check only ran when the WHOLE merge was empty, a partially
	// healed corpus rendered "Searched 8 practice graphs" while graphs with no index
	// contributed nothing and were never named. Measured live: that header listed
	// eight graphs while three of them had zero segments.
	var (
		mu        sync.Mutex
		all       []practiceGraphResult
		failed    []string
		unindexed []string
		wg        sync.WaitGroup
	)
	gc := deps.GraphCaller()
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for _, name := range names {
		wg.Add(1)
		go func(language string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results, err, gapped := practiceFanOutProbe(ctx, deps, mgr, gc, language, query, queryVec, k)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, language+": "+err.Error())
			case len(results) > 0:
				all = append(all, practiceGraphResult{graph: language, results: results})
			case gapped:
				unindexed = append(unindexed, language)
			}
		}(name)
	}
	wg.Wait()
	sort.Strings(failed)
	sort.Strings(unindexed)

	merged := mergePracticeFanOutHits(all, k)

	// The arm disclosure, computed from what actually reached the engine. One failed
	// embed degrades EVERY graph in this fan-out, so the label covers the whole
	// merged ranking rather than any single graph.
	modeLabel := segmentSearchModeLabel(query != "", len(queryVec) > 0)

	// Nothing merged: qualify the zero BEFORE rendering it.
	if len(merged) == 0 {
		if res, done := practiceFanOutZeroResult(ctx, deps, names, failed, embErr); done {
			return res
		}
	}

	if format == "json" {
		// Flatten each merged hit's .Result into the flat json envelope. The
		// per-graph identity already RIDES each h.Result: hydrateEngineHits
		// stamps {practice, <language>} per per-graph hydrate call, so the json
		// consumer (graph-UI) traverses each result in its own language. The .Graph
		// markdown attribution is now redundant with the stamp; the renderJSON shape
		// stays flat.
		flat := make([]engine.SearchResult, len(merged))
		for i, h := range merged {
			flat[i] = h.Result
		}
		return engine.RenderForCaller(query, flat, "json", fields, modeLabel)
	}

	searched := make([]string, len(names))
	copy(searched, names)
	sort.Strings(searched)
	// A PARTIAL cross-graph ranking presented as complete is the same lie in a
	// quieter register: the caller cannot tell a graph with no matches from a graph
	// that errored or one with no index at all, so both are named alongside the
	// results. The header says "Searched N practice graphs" — this is what keeps
	// that sentence true.
	return appendNotice(engine.RenderPracticeFanOut(query, searched, merged, modeLabel),
		practiceFanOutPartialLine(failed, unindexed))
}

// practiceSearchOneGraph searches ONE practice graph and hydrates its hits — the
// body of the fan-out's per-graph goroutine, lifted out so the composer stays
// inside the cognitive-complexity budget.
//
// IT DISTINGUISHES NO-MATCH FROM FAILURE, which is the whole point of the return
// shape: (nil, nil) means the graph was searched successfully and simply had
// nothing, while a non-nil error means it could NOT be searched. Collapsing the
// two is what let the fan-out report a confident no-match over a lane where every
// graph had errored.
func practiceSearchOneGraph(
	ctx context.Context, mgr SegmentSearcher, gc GraphCaller,
	language, query string, queryVec []byte, k int,
) ([]engine.SearchResult, error) {
	hits, err := mgr.Search(ctx, kgtypes.GraphPractice, language, query, queryVec, k)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	return hydrateEngineHits(ctx, gc, hydrateSelector{Graph: "practice", Language: language}, hits)
}

// practiceQueryText picks the search text from the query/text fields.
func practiceQueryText(a queryArgs) string {
	if a.Text != "" {
		return a.Text
	}
	if len(a.Queries) > 0 {
		return a.Queries[0]
	}
	return ""
}

// rankedSearchRetiredResult is the "ranked text search is not offered" result
// for the LINKAGE graph, the one graph that has no ranked search.
//
// The reason is specific to linkage and does not generalize: a linkage graph
// DENORMALIZES text from the graphs it links, so its rows carry no content of
// their own to rank — searching it would return the same text the source graph
// already answers for, attributed to a proxy. Its index-free ops — list-graphs,
// stats, get-node-by-id, traverse, proxy read-through and browse — are
// UNAFFECTED.
//
// The graph label is still a parameter so the message names the graph it is
// answering for. Defined here, reused by both tools.
func rankedSearchRetiredResult(graph string) kgtools.ToolResult {
	return textResult(fmt.Sprintf(
		"Ranked text search for the %s graph is retired. The %s graph carries no "+
			"unique client-indexable content, so it has no ranked search index. "+
			"Its other operations still work: list-graphs, stats, get-node-by-id, "+
			"traverse, and browse.",
		graph, graph))
}

// listPracticeGraphs enumerates the loaded practice graphs (RETURN_MODE_GRAPH_NAMES
// Execute via listGraphNamesOfType + per-graph Stats counts).
func listPracticeGraphs(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	names, err := listGraphNamesOfType(ctx, deps, "practice")
	if err != nil {
		return errorResult("practice list-graphs failed: " + err.Error())
	}
	if len(names) == 0 {
		return textResult("No practice graphs found.")
	}
	gc := deps.GraphCaller()
	sc, ok := gc.(statsRPC)
	if !ok {
		return errorResult("practice list-graphs: stats seam unavailable")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Practice graphs (%d):\n\n", len(names))
	for _, name := range names {
		nodes, edges := graphCounts(ctx, sc, "practice", name)
		fmt.Fprintf(&sb, "- **%s** — %d nodes, %d edges\n", name, nodes, edges)
	}
	sb.WriteString("\nUse `query({ \"graph\": \"practice\", \"language\": \"go\" })` to browse a specific practice graph.")
	return textResult(sb.String())
}
