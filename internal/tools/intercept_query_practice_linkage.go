// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_practice_linkage.go is the client-side claim for the practice
// and linkage per-graph query shapes the retired server-side query routing
// served, plus the web/pdf and checks arms that joined it later.
//
// practice shapes:
//   - list-graphs   (no language): enumerate practice graphs
//     (RETURN_MODE_GRAPH_NAMES Execute + Stats RPC counts).
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Practice Graph: <lang>").
//   - browse        (language, no text): type/types/status/meta filters + paging
//     lowered onto ONE Selection → practiceBrowse (intercept_query_practice_browse.go).
//   - style_index   (mode): the compact style-rule index — one drained
//     hub-and-kind browse, a client-side scope filter, one line per rule
//     → practiceStyleIndex (style_rule_index.go).
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
//   - ranked text search: served from the CLIENT SEGMENT ENGINE by
//     composeRawGraphSegmentSearch (raw_graph_segment_search.go) — raw graphs are
//     enrolled embed-only, so their chunks carry BOTH a vector and a BM25
//     document and the shipped segments are simply asked. Rendered with heading
//     context, and the footer discloses which arms actually ran.
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
		// A LANGUAGE-LESS BY-ID READ IS NOW SERVED, not refused. It used to name no
		// graph — the family keyed its instance by language — so the only honest
		// answer was a refusal saying which call worked. The family holds ONE graph
		// now, so an unselected by-id read resolves it, and the arms below serve it
		// like every other unselected practice read.
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

// routePracticeClient dispatches the five practice shapes. raw is the caller's
// verbatim payload, threaded explicitly (rather than stashed on queryArgs) so
// the per-arm accounting gate cannot be forgotten at a claim point.
func routePracticeClient(ctx context.Context, deps ClientDeps, gc statsRPC, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	// (0) The RETIRED fan-out sentinel is refused BEFORE anything else reads the
	// payload, because "all" is a value the arms below would otherwise treat as a
	// legacy graph name and resolve to a graph that does not exist. Refusing first
	// costs no read, no embed and no wire call, and answers with the call that
	// works rather than with a not-found.
	if a.Language == "all" {
		return errorResult(practiceFanOutRetired)
	}
	// (1) mode=stats.
	if a.Mode == "stats" {
		if err := accountQueryParams(armPracticeStats, raw); err != nil {
			return errorResult(err.Error())
		}
		return practiceStatsResult(ctx, gc, a)
	}
	// (1b) mode=modules is the LEGACY ENUMERATION of the pre-singleton graphs.
	// It used to be what an empty selector meant; an empty selector now browses
	// the combined graph, so the enumeration has to be asked for by name.
	if a.Mode == "modules" {
		if err := accountQueryParams(armPracticeListGraphs, raw); err != nil {
			return errorResult(err.Error())
		}
		return listPracticeGraphs(ctx, deps)
	}
	// (1c) mode=style_index is the COMPACT STYLE-RULE INDEX. It sits above the
	// browse gate because it carries no text either, so the browse would
	// otherwise claim it — and above the text gate for the same reason
	// mode:"modules" is: a mode this arm serves is asked for by name.
	if a.Mode == "style_index" {
		if err := accountQueryParams(armPracticeStyleIndex, raw); err != nil {
			return errorResult(err.Error())
		}
		return practiceStyleIndex(ctx, gc.Execute, a)
	}
	// (2) No ranked text → BROWSE. With no language that is the combined graph,
	// optionally narrowed to one hub by `source`; with a language it is the legacy
	// graph, unchanged.
	if practiceQueryText(a) == "" {
		if err := accountQueryParams(armPracticeBrowse, raw); err != nil {
			return errorResult(err.Error())
		}
		return practiceBrowse(ctx, gc.Execute, a)
	}

	// (3) ranked search.
	query := practiceQueryText(a)

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
	return composePracticeSearchClient(ctx, deps, deps.SegmentManager(), a.Language, a.Source, query, a.Format, int(a.Limit), a.Fields)
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
	language, hub, query, format string, limit int, fields []string,
) kgtools.ToolResult {
	// Readiness gate (bind-first startup): mgr.Search below dereferences the segment Manager
	// with no nil-check; during the bind-first wiring window SegmentManager() is an
	// untyped nil → panic. Gate before the deref. Both entry points (the practice
	// browse arm in this file and the search-tool arm) funnel through here.
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

	// THE SEGMENT POOL IS NAMED BY THE INTERNAL KEYING RULE, NOT BY THE WIRE ONE.
	// An unselected practice search reads the combined graph, whose pool is sealed
	// under the canonical instance name while every wire read of it sends an empty
	// selector — the two namespaces diverge exactly as they do for checks, and
	// asking the engine for "" would search an instance nothing ever wrote to and
	// return a confident zero.
	pool := workingset.CanonicalInstanceName(kgtypes.GraphPractice, language)

	hits, err := practiceRankedHits(ctx, deps, mgr, pool, hub, query, queryVec, k)
	if err != nil {
		return errorResult("practice search: " + err.Error())
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
		// THE POOL NAME, NOT THE CALLER'S LANGUAGE. The gap probe reads the graph
		// by name; an unselected search has no language, and probing practice/""
		// would find no graph and report a gap that is really an empty selector.
		notice, loud := practiceZeroHitNotice(ctx, deps, pool, embErr)
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

// THE SCATTER-GATHER FAN-OUT IS GONE, and its absence is the point rather than
// an omission. composePracticeSearchFanOut and practiceSearchOneGraph searched
// every loaded practice graph in parallel and merged the per-graph hits under
// per-graph attribution, because the corpus was eight graphs and `language:"all"`
// was how a caller reached all of it. The corpus is ONE graph now: an unselected
// search already reads the whole of it in one engine call, so the fan-out had no
// caller left and the sentinel that reached it is refused by name
// (practiceFanOutRetired). Keeping the machinery would have left a second,
// slower path to the same answer for the next reader to choose between.
//
// The three-bucket outcome discipline it carried — matched, failed, unindexed,
// never collapsed into one zero — survives in practiceZeroHitNotice, which is
// what qualifies a zero-hit practice search now.

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
		// THE LEGACY TARGET, BUILT EXPLICITLY. This enumeration lists the
		// pre-singleton graphs, and a derived practice target carries no instance
		// field — every row would report the COMBINED graph's counts under a
		// different graph's name.
		sb.WriteString(graphCountRowFor(ctx, sc, name, practiceReadTarget(name)))
	}
	// THE HINT NAMES THE SELECTOR EACH ROW ACTUALLY TAKES. The combined graph is
	// browsed with no instance field at all and narrowed by `source`; only a
	// PRE-SINGLETON row is reachable by `language`, which is read-only. A single
	// `language` hint sent a reader at a graph this enumeration had just listed
	// under a name that selector cannot resolve.
	sb.WriteString("\nBrowse the combined graph with `query({ \"graph\": \"practice\" })`, narrowed to one origin by `source`: `query({ \"graph\": \"practice\", \"source\": \"<hub id>\" })`. A row above that names a PRE-SINGLETON graph is read with the legacy read-only selector: `query({ \"graph\": \"practice\", \"language\": \"<that name>\" })`.")
	return textResult(sb.String())
}
