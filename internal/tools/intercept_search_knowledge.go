// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

// knowledgeSearchArgs is the slice of the search payload the client-engine
// knowledge arm consumes: the query text, the (client-embedded) query vector,
// the limit, the requested render format/fields, and the optional node-type
// post-filter. Mirrors the fields compileSearch read for the server path.
//
// Mode/HalfLife carry the query-tool temporal arm (mode=recent): when Mode is
// "recent" the hydrated rows get a client UpdatedAt half-life rerank
// (applyTemporalRerank) before render, mirroring the server temporal boost. The
// SEARCH tool never sets a mode, so its knowledge arm leaves Mode empty (no
// rerank). HalfLife defaults to recentTemporalHalfLifeDays when unset.
type knowledgeSearchArgs struct {
	Query       string   `json:"query"`
	QueryVector []byte   `json:"query_vector"`
	Limit       int      `json:"limit"`
	Format      string   `json:"format"`
	Fields      []string `json:"fields"`
	Types       []string `json:"types"`
	Mode        string   `json:"mode"`
	HalfLife    float64  `json:"half_life"`
}

// recentTemporalHalfLifeDays is the default half-life for the mode=recent
// temporal rerank — mirrors the engine recentHalfLifeDays / the server's 30-day
// floor (temporal_rerank.go computeTemporalScore halfLife<=0 → 30).
const recentTemporalHalfLifeDays = 30.0

// InterceptQueryKnowledgeSearch claims the query-tool knowledge text-search modes
// that were previously compiled to a server RETURN_MODE_SEARCH dispatch and routes
// them through the CLIENT knowledge engine (composeKnowledgeSearch).
// mode=recent has TWO arms: text-bearing recent runs the client search with a
// client UpdatedAt half-life rerank (composeKnowledgeSearch); BARE recent (empty
// text) is a pure temporal browse over GraphCaller (composeRecentBrowse) — no
// search query, just most-recently-updated nodes, optionally scoped by `types`.
// Returns (false,_) for any other tool/graph/mode — and for empty-text text/default
// modes — so the chain proceeds. The query tool carries the search text in `text`
// (not `query`); this claim maps it onto the knowledgeSearchArgs `query` field
// composeKnowledgeSearch reads.
func InterceptQueryKnowledgeSearch(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if !isKnowledgeDefaultGraph(a.Graph) {
		return false, kgtools.ToolResult{}
	}
	if hasThoughtQueryFilter(a) {
		return false, kgtools.ToolResult{} // recall/reflect shape stays on the thoughts surface.
	}
	// Claimed knowledge text-search shapes: mode=recent (temporal rerank), mode=text,
	// and the DEFAULT mode (empty) carrying ONLY a text query (no id/ids/type/meta —
	// those non-search default shapes stay on the compileQuery browse/getNode path).
	mode, claimed := knowledgeSearchModeFor(a)
	if !claimed {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	ctx := context.Background()
	if a.Text == "" {
		// Empty-text recent is a pure temporal BROWSE (no search query): fetch the
		// most-recently-updated nodes via GraphCaller and rerank by UpdatedAt. Every
		// other empty-text mode (text/default) still bails so precheck/deny owns the
		// requires-text message.
		if mode != "recent" {
			return false, kgtools.ToolResult{} // empty text → precheck/deny owns the message.
		}
		if gc == nil {
			return true, errorResult("recent browse: graph client unavailable")
		}
		return true, composeRecentBrowse(ctx, gc, a)
	}
	if gc == nil {
		return true, errorResult(mode + " search: graph client unavailable")
	}
	// Readiness gate (bind-first startup): composeKnowledgeSearch dereferences the segment
	// Manager (mgr.Search) with no nil-check; during the bind-first wiring window
	// SegmentManager() is an untyped nil → panic. Gate before the deref.
	if !deps.PipelineReady() {
		return true, errorResult("knowledge search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	halfLife := 0.0
	if mode == "recent" {
		halfLife = recentTemporalHalfLifeDays
	}
	return true, composeKnowledgeSearch(ctx, gc, deps.SegmentManager(),
		knowledgeQueryToSearchArgs(ctx, deps, a, mode, halfLife))
}

// composeRecentBrowse serves bare query(mode:recent) (empty text) as a temporal
// browse: it fetches the candidate node set over GraphCaller (type-scoped when
// `types` is set, else every node), maps each node to a unit-score SearchResult,
// applies the UpdatedAt half-life rerank verbatim, then truncates to the limit
// AFTER the sort and renders. Because every base score is 1.0, applyTemporalRerank's
// UpdatedAt boost is the SOLE ordering signal → pure most-recently-updated order.
//
// The type filter is pushed to the FETCH: a Selection.NodeTypes-bearing browse
// plan is trimmed to that type set server-side by postFilterBrowseNodeTypes
// (cmd/knowledge-server/internal/bootstrap/engine_normalize.go) BEFORE responding —
// the same mechanism the plural-types browse arm uses (no client-side fetch-all).
// The plan carries NO Limit (Limit 0 = no cap) so every recency-eligible node is
// considered before the sort; the limit is honored only after ordering.
func composeRecentBrowse(ctx context.Context, gc GraphCaller, a queryArgs) kgtools.ToolResult {
	selection := &knowledgev1.Selection{}
	if len(a.Types) > 0 {
		selection = &knowledgev1.Selection{NodeTypes: a.Types}
	}
	plan := &knowledgev1.QueryPlan{
		Selection:         selection,
		IncludeTombstones: a.IncludeTombstones,
	}
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: domainTarget(a),
	})
	if err != nil {
		return errorResult("recent browse: fetch: " + err.Error())
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return errorResult("recent browse: decode: " + derr.Error())
	}

	results := make([]engine.SearchResult, len(nodes))
	for i, n := range nodes {
		// Stamp the source-graph identity: this is the knowledge default
		// graph (no instance). composeRecentBrowse builds SearchResults directly
		// (it is a temporal BROWSE, not a hydrateEngineHits funnel), so it stamps
		// here rather than inheriting the hydrate stamp.
		results[i] = engine.SearchResult{Node: n, Score: 1.0, Graph: string(kgtypes.GraphKnowledge)}
	}

	// UpdatedAt half-life rerank (BOOST + re-sort); base scores are all 1.0 so the
	// resulting order is pure most-recently-updated.
	applyTemporalRerank(results, recentTemporalHalfLifeDays)

	// Truncate AFTER the sort — truncating the fetch would bias which nodes are
	// considered (mirrors composeTimeline's render-output limit-after-sort).
	k := int(a.Limit)
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	if len(results) > k {
		results = results[:k]
	}

	format := a.Format
	if format == "" {
		format = "text"
	}
	return engine.RenderForCaller("", results, format, a.Fields, "recency-boosted")
}

// knowledgeSearchModeFor reports whether a knowledge-graph query is one of the
// claimed text-search shapes and returns the composeKnowledgeSearch mode to use:
//   - mode=recent → "recent" (temporal rerank).
//   - mode=text   → "text".
//   - default mode (empty) carrying a text query AND no id/ids/type/meta-only
//     browse shape → "text" (the default-text search arm).
//
// Returns ("", false) for every non-search default shape (id getNode, ids[] bulk,
// type-browse, meta-only) so compileQuery keeps owning those.
func knowledgeSearchModeFor(a queryArgs) (string, bool) {
	switch a.Mode {
	case "recent":
		return "recent", true
	case "text":
		return "text", true
	case "":
		// Default mode: claim ONLY the pure text-search shape. Any id / ids / type
		// / meta-only signal means this is a browse/getNode read, not a search.
		if a.Text != "" && a.ID == "" && len(a.IDs) == 0 && a.Type == "" && len(a.Meta) == 0 {
			return "text", true
		}
		return "", false
	default:
		return "", false
	}
}

// hasThoughtQueryFilter reports whether a query carries a thought-graph filter
// field — the recall/reflect surface owns those, not the knowledge search arm.
// Mirrors the engine hasThoughtFilter gate so the knowledge-search claim never
// swallows a recall-shaped query.
func hasThoughtQueryFilter(a queryArgs) bool {
	return a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != ""
}

// knowledgeQueryToSearchArgs builds the knowledgeSearchArgs JSON the
// composeKnowledgeSearch arm consumes from a query-tool queryArgs: it maps text→
// query, embeds the query client-side (so the HNSW arm is exercised), and carries
// the mode + half-life for the temporal rerank. A nil embedder leaves the vector
// empty (BM25-only via RRF-over-one-list).
func knowledgeQueryToSearchArgs(ctx context.Context, deps ClientDeps, a queryArgs, mode string, halfLife float64) json.RawMessage {
	out := map[string]any{
		"query":  a.Text,
		"limit":  int(a.Limit),
		"format": a.Format,
		"fields": a.Fields,
		"mode":   mode,
	}
	// The query tool's singular `type` maps onto the knowledge-search plural
	// node-type post-filter when set.
	if a.Type != "" {
		out["types"] = []string{a.Type}
	}
	if halfLife > 0 {
		out["half_life"] = halfLife
	}
	if emb := deps.Embedder(); emb != nil && a.Text != "" {
		if vec, err := emb.EmbedBinary(ctx, a.Text); err == nil && len(vec) > 0 {
			out["query_vector"] = vec
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return params0RawForKnowledgeArgs(a.Text)
	}
	return raw
}

// params0RawForKnowledgeArgs is the fail-soft fallback when the args re-marshal
// fails (should never happen for a string-keyed map) — a minimal {query} payload
// so the search still runs BM25-only rather than erroring. Uses the typed
// knowledgeSearchArgs (no `any` map) so the marshal is statically safe.
func params0RawForKnowledgeArgs(text string) json.RawMessage {
	raw, err := json.Marshal(knowledgeSearchArgs{Query: text})
	if err != nil {
		return json.RawMessage(`{"query":""}`)
	}
	return raw
}

// composeKnowledgeSearch runs the knowledge/default search arm against the
// CLIENT engines (Manager.Search → RRF fusion) + RETURN_MODE_NODES hydration,
// instead of dispatching a server RETURN_MODE_SEARCH. The query vector was
// embedded client-side upstream (maybeEmbedQuery) so the HNSW arm is exercised;
// a missing embedder leaves QueryVector empty and Manager.Search degrades to the
// BM25 arm via RRF-over-one-list. Returns a rendered ToolResult in the caller's
// format — the InterceptSearch rerank gate then hydrates + reranks the JSON
// envelope exactly as it did for the server path.
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
	return engine.Dispatch(ctx, gc.Execute, "search", args)
}

func composeKnowledgeSearch(
	ctx context.Context,
	gc GraphCaller,
	mgr SegmentSearcher,
	args json.RawMessage,
) kgtools.ToolResult {
	var a knowledgeSearchArgs
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

	hits, err := mgr.Search(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, a.Query, a.QueryVector, k)
	if err != nil {
		return errorResult("knowledge search: client engine: " + err.Error())
	}

	results, err := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: string(kgtypes.GraphKnowledge)}, hits)
	if err != nil {
		return errorResult("knowledge search: hydrate: " + err.Error())
	}

	// node_types post-filter: the server applied this on the ranked set
	// (compileSearch Selection.NodeTypes); reproduce it client-side over the
	// fused+hydrated rows so the type filter still narrows the result.
	if len(a.Types) > 0 {
		results = filterResultsByNodeTypes(results, a.Types)
	}

	// mode=recent: apply the client UpdatedAt half-life rerank (BOOST + re-sort)
	// over the hydrated rows, mirroring the server temporal rerank EXACTLY. The
	// SEARCH tool leaves Mode empty, so this is a no-op there.
	if a.Mode == "recent" {
		applyTemporalRerank(results, a.HalfLife)
	}

	format := a.Format
	if format == "" {
		format = "text"
	}
	return engine.RenderForCaller(a.Query, results, format, a.Fields, "vector+text")
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
