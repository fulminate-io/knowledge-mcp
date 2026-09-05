// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// knowledgeDefaultName is the single instance name the knowledge graph keys its
// client-hosted segments under (mirrors SegmentGraphKey's knowledge arm:
// formatGraphKey(GraphKnowledge, "default")).
const knowledgeDefaultName = "default"

// knowledgeSearchDefaultLimit is the fallback top-k when the caller supplies no
// limit — matches the savedState default (10) the server search path used.
const knowledgeSearchDefaultLimit = 10

// isKnowledgeDefaultGraph reports whether a search/graph value targets the
// knowledge graph's default instance — an empty graph (the engine treats ""
// as knowledge) or the explicit "knowledge". This is the ONE arm GO-LIVE
// reroutes to the client engine in this step; cloud/cicd/practice/code are
// handled elsewhere.
func isKnowledgeDefaultGraph(graph string) bool {
	return graph == "" || graph == "knowledge"
}

// segmentSearchArgs is the slice of the search payload a client-engine SEGMENT
// search arm consumes: the query text, the (client-embedded) query vector, the
// limit, the requested render format/fields, and the optional node-type
// post-filter. Mirrors the fields compileSearch read for the server path. It is
// not knowledge-specific — both segment arms decode into it, the
// knowledge/default arm here and the registered custom-graph arm in
// intercept_search_registered_graph.go, so neither can drift about which wire
// fields exist.
//
// Mode selects which retrieval arms run and whether the recency rerank applies;
// HalfLife tunes that rerank. Mode now arrives faithfully from BOTH tools — the
// search tool decodes it straight off the wire, and the query tool writes the
// resolved execution mode into the payload it builds — so neither tool's callers
// get a mode the other would have honored.
//
// normalizeSegmentSearchMode (search_mode_contract.go) is the ONE place the
// declared equivalences are resolved: an absent mode to hybrid, and temporal to
// recent. Consumers here read the normalized value rather than re-deriving it.
// HalfLife defaults to recentTemporalHalfLifeDays when unset.
type segmentSearchArgs struct {
	Query       string   `json:"query"`
	QueryVector []byte   `json:"query_vector"`
	Limit       int      `json:"limit"`
	Format      string   `json:"format"`
	Fields      []string `json:"fields"`
	Types       []string `json:"types"`
	// Meta is a QUERY-TOOL-ONLY carrier: the search tool publishes no `meta`
	// param at all, so it decodes empty on every search-tool arm and the metadata
	// post-filter is a no-op there. Parity by construction, not coincidence.
	Meta     map[string]string `json:"meta,omitempty"`
	Mode     string            `json:"mode"`
	HalfLife float64           `json:"half_life"`
}

// recentTemporalHalfLifeDays is the default half-life for the mode=recent
// temporal rerank — mirrors the engine recentHalfLifeDays / the server's 30-day
// floor (temporal_rerank.go computeTemporalScore halfLife<=0 → 30).
const recentTemporalHalfLifeDays = 30.0

// composeKnowledgeSearch runs the knowledge/default search arm against the
// CLIENT engines (Manager.Search → RRF fusion) + RETURN_MODE_NODES hydration,
// instead of dispatching a server RETURN_MODE_SEARCH. The query vector was
// embedded client-side upstream (maybeEmbedQuery) so the HNSW arm is exercised;
// a missing embedder leaves QueryVector empty and Manager.Search degrades to the
// BM25 arm via RRF-over-one-list. Returns a rendered ToolResult in the caller's
// format — the InterceptSearch rerank gate then hydrates + reranks the JSON
// envelope exactly as it did for the server path. Its post-hydrate tail is
// finishSegmentSearchRender, shared with the registered custom-graph arm so the
// two segment-search arms cannot drift on filtering, reranking, field projection
// or the search-mode footer.
// runKnowledgeOrServerSearch routes the search tail: the knowledge/default arm
// runs against the CLIENT BM25+HNSW engines (composeKnowledgeSearch →
// Manager.Search → RRF + RETURN_MODE_NODES hydration). The segment Manager is
// wired for the life of the daemon EXCEPT during the bind-first wiring window
// (bind-first startup) — the caller (search.go and InterceptQueryKnowledgeSearch) gates the
// knowledge arm on PipelineReady before reaching here, so during the window a
// not-ready error is returned instead of a nil-Manager deref; there is no
// server-dispatch fallback for knowledge. Any other graph that reaches here
// (non-knowledge with a rewrite/embed that passed the claim gate) still rides the
// server RETURN_MODE_SEARCH dispatch; the per-graph claims own those arms upstream.
// The embed step upstream already set query_vector so the HNSW arm is exercised;
// the caller's rerank gate is unchanged (it hydrates the rendered JSON envelope
// identically either way).
func runKnowledgeOrServerSearch(
	ctx context.Context,
	deps ClientDeps,
	gc GraphCaller,
	graph string,
	args json.RawMessage,
) (kgtools.ToolResult, error) {
	if isKnowledgeDefaultGraph(graph) {
		// Readiness gate (bind-first startup): composeKnowledgeSearch dereferences the segment
		// Manager with no nil-check; during the bind-first wiring window
		// SegmentManager() is an untyped nil → panic. Gate before the deref. This is
		// the single chokepoint for the search-tool knowledge arm; the query-tool arm
		// (InterceptQueryKnowledgeSearch) carries its own pre-check.
		if !deps.PipelineReady() {
			return errorResult("knowledge search: daemon still starting — LLM pipeline not ready yet, retry shortly"), nil
		}
		return composeKnowledgeSearch(ctx, gc, deps.SegmentManager(), args), nil
	}
	// A MISSING STATS SEAM IS RETURNED AS AN ERROR, not folded into a ToolResult
	// here. Every OTHER statsFnOf caller (query.go, intercept_mutate_dispatch.go,
	// intercept_mutate_link.go) sits in a function whose ONLY return is a
	// ToolResult, so converting is the only thing those can do. This function also
	// returns an error, and the very next line propagates engine.Dispatch's error
	// as an error — so converting here was the one asymmetry, and it read as an
	// error observed and then dropped. The failure still reaches the user
	// unchanged in substance: interceptSearchArms renders a non-nil error from
	// this function into the same IsError ToolResult, as
	// "search call failed: <msg>", mirroring query.go's dispatch arm.
	stats, serr := statsFnOf(gc)
	if serr != nil {
		return kgtools.ToolResult{}, serr
	}
	return engine.Dispatch(ctx, gc.Execute, stats, "search", args)
}

func composeKnowledgeSearch(
	ctx context.Context,
	gc GraphCaller,
	mgr SegmentSearcher,
	args json.RawMessage,
) kgtools.ToolResult {
	var a segmentSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return errorResult("knowledge search: decode args: " + err.Error())
	}
	// Permanent-degrade guard (bind-first startup): the PipelineReady gate upstream rejects
	// the wiring window, but markPipelineReady is set even when wirePipelineRuntime
	// DEGRADED (no embedder/summarizer, or a wire error → segment Manager never
	// built). In that case PipelineReady()==true but the Manager is nil here; guard
	// the deref with a loud error instead of a nil-Search panic. There is no server
	// search fallback for knowledge (the retired path returns 0 hits).
	if mgr == nil {
		return errorResult("knowledge search: client segment engine unavailable (LLM pipeline degraded at boot)")
	}
	k := a.Limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}

	mode := normalizeSegmentSearchMode(a.Mode)
	engineText, engineVec := segmentSearchEngineArms(mode, a.Query, a.QueryVector)
	if mode == "vector" && len(engineVec) == 0 {
		// Serving this renders zero rows, which a caller reads as "no matches"
		// when the truth is "this install has no semantic index".
		return errorResult("knowledge search: mode:vector needs a query embedding, " +
			"but no embedder is configured — use mode:hybrid or mode:text instead")
	}

	hits, err := mgr.Search(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, engineText, engineVec, k)
	if err != nil {
		return errorResult("knowledge search: client engine: " + err.Error())
	}

	results, err := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: string(kgtypes.GraphKnowledge)}, hits)
	if err != nil {
		return errorResult("knowledge search: hydrate: " + err.Error())
	}

	// The render header carries the CALLER's query, not the engine text: under
	// mode:vector the engine text is empty, but the caller still asked for that
	// query and the response header is where they look to confirm it.
	return finishSegmentSearchRender(a.Query, results, a, mode, engineText, engineVec)
}

// finishSegmentSearchRender is the post-hydrate tail BOTH client-engine segment
// search arms share: the node-type post-filter, the metadata post-filter, the
// mode=recent temporal rerank, the format default and the render. Extracted so
// the knowledge/default arm above and the registered custom-graph arm
// (composeRegisteredGraphSearch) cannot drift on filtering, reranking, field
// projection or the search-mode footer — every one of those was a block one arm
// had and the other did not, for exactly as long as each owned its own tail.
//
// query is a SEPARATE parameter rather than a.Query because a search-tool call
// site supplies a queries[]-merged text that differs from the decoded query
// field. mode is the ALREADY-NORMALIZED execution mode — normalized once by the
// caller and reused here, never re-derived, so the two cannot disagree.
//
// engineText and engineVec are what actually reached the segment engine, and the
// footer label is computed from THEM rather than from a.Query/a.QueryVector.
// That distinction is the whole point: under mode:text the raw QueryVector can
// still be populated even though the arm dropped it, and under mode:vector the
// raw Query is non-empty even though the arm emptied the engine text — so
// re-reading the args would announce a BM25 contribution that never happened.
//
// The ONE ordering constraint below is a data dependency: the format default
// must run before the render that consumes it. The two filters and the rerank
// commute — both filters are order-preserving subset selections and
// applyTemporalRerank is a per-row score multiply followed by a STABLE sort — so
// this order carries no semantics beyond being the order the knowledge arm
// already had.
func finishSegmentSearchRender(
	query string, results []engine.SearchResult, a segmentSearchArgs,
	mode, engineText string, engineVec []byte,
) kgtools.ToolResult {
	// node_types post-filter: the server applied this on the ranked set
	// (compileSearch Selection.NodeTypes); reproduce it client-side over the
	// fused+hydrated rows so the type filter still narrows the result.
	if len(a.Types) > 0 {
		results = filterResultsByNodeTypes(results, a.Types)
	}

	// metadata post-filter, for the same reason as the type filter above: a
	// metadata predicate does not compose with a ranked search server-side,
	// because the search over-fetches and ranks BEFORE metadata is consulted.
	if len(a.Meta) > 0 {
		results = filterResultsByMetadata(results, a.Meta)
	}

	// The recency arm: apply the client UpdatedAt half-life rerank (BOOST +
	// re-sort) over the hydrated rows, mirroring the server temporal rerank
	// EXACTLY. Keyed on the NORMALIZED mode, so the declared temporal spelling
	// reranks exactly like recent does instead of silently doing nothing.
	if mode == "recent" {
		applyTemporalRerank(results, a.HalfLife)
	}

	format := a.Format
	if format == "" {
		format = "text"
	}
	return engine.RenderForCaller(query, results, format, a.Fields,
		segmentSearchModeLabel(engineText != "", len(engineVec) > 0))
}

// applyTemporalRerank reranks hydrated rows by an UpdatedAt half-life BOOST,
// mirroring the server ApplyTemporalReranking / computeTemporalScore EXACTLY
// (cmd/knowledge-server/internal/store/temporal_rerank.go:16-44 — re-implemented
// here as a fresh client helper, NOT a relocation: the server file stays present
// and compiling, merely unreached):
//
//   - temporal = 2^(-ageDays/halfLife), ageDays = time.Since(UpdatedAt)/24h.
//   - UpdatedAt IsZero (no timestamp) → temporal = 0.5 (neutral).
//   - halfLife <= 0 → halfLife = 30 (floor).
//   - score *= (1 + temporal)  — a BOOST, NOT a replacement.
//   - re-sort by the boosted score, descending.
func applyTemporalRerank(results []engine.SearchResult, halfLife float64) {
	for i := range results {
		results[i].Score *= 1.0 + computeTemporalScore(results[i].Node.GetUpdatedAt(), halfLife)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
}

// computeTemporalScore returns the half-life decay factor for a node's
// UpdatedAt (unix-nanos), mirroring the server computeTemporalScore: a zero/
// IsZero timestamp is neutral (0.5); a non-positive half-life floors to 30 days.
func computeTemporalScore(updatedAtNanos int64, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		halfLifeDays = recentTemporalHalfLifeDays
	}
	if updatedAtNanos == 0 {
		return 0.5 // neutral score for nodes with no timestamp (IsZero).
	}
	ageDays := time.Since(time.Unix(0, updatedAtNanos)).Hours() / 24.0
	return math.Pow(2.0, -ageDays/halfLifeDays)
}

// filterResultsByNodeTypes trims hydrated rows to those whose node Type is in
// the allowed set (mirrors the engine's Selection.NodeTypes post-filter). An
// empty allow-set is a no-op.
func filterResultsByNodeTypes(results []engine.SearchResult, types []string) []engine.SearchResult {
	if len(types) == 0 {
		return results
	}
	allow := make(map[string]bool, len(types))
	for _, t := range types {
		allow[t] = true
	}
	filtered := make([]engine.SearchResult, 0, len(results))
	for _, r := range results {
		if allow[r.Node.GetType()] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// filterResultsByMetadata trims hydrated rows to those whose node metadata
// satisfies every supplied predicate. Mirrors filterResultsByNodeTypes above,
// and exists for the same reason: a metadata predicate cannot be pushed into a
// ranked search, because the search ranks before metadata is consulted.
//
// Semantics match the metadata predicate the browse path uses and the query
// schema advertises: a value of "*" requires the key PRESENT AND NON-EMPTY, any
// other value requires exact equality, and multiple keys are AND'd. An empty
// filter is a no-op.
func filterResultsByMetadata(results []engine.SearchResult, meta map[string]string) []engine.SearchResult {
	if len(meta) == 0 {
		return results
	}
	filtered := make([]engine.SearchResult, 0, len(results))
	for _, r := range results {
		nodeMeta := r.Node.GetMetadata()
		match := true
		for key, want := range meta {
			got, present := nodeMeta[key]
			if want == "*" {
				if !present || got == "" {
					match = false
					break
				}
				continue
			}
			if got != want {
				match = false
				break
			}
		}
		if match {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
