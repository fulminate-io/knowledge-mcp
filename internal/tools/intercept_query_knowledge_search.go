// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// InterceptQueryKnowledgeSearch claims the query-tool knowledge text-search modes
// that were previously compiled to a server RETURN_MODE_SEARCH dispatch and routes
// them through the CLIENT knowledge engine (composeKnowledgeSearch).
// mode=recent has TWO arms: text-bearing recent runs the client search with a
// client UpdatedAt half-life rerank (composeKnowledgeSearch); BARE recent (empty
// text) is a pure temporal browse over GraphCaller (composeRecentBrowse) — no
// search query, just most-recently-updated nodes, optionally scoped by `types`.
// Returns (false,_) for any other tool/graph/mode — and for empty-text text/default
// modes — so the chain proceeds. The query tool carries the search text in `text`
// (not `query`); this claim maps it onto the segmentSearchArgs `query` field
// composeKnowledgeSearch reads.
func InterceptQueryKnowledgeSearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
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
	// knowledgeSearchModeFor is the authoritative definition of which shapes this
	// arm claims — read it there, not here. A claim means this arm serves the
	// call; a decline leaves it to the compileQuery browse/getNode path.
	//
	// Deliberately NOT re-stated: a paraphrase of a routing gate is a second
	// derivation of the rule, and it rots the moment the predicate moves. The
	// enumeration that used to sit here had gone stale exactly that way, and a
	// reader who trusted it drew the wrong conclusion about which shapes reach
	// this arm. When describing a gate, point at the predicate.
	mode, claimed := knowledgeSearchModeFor(a)
	if !claimed {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()

	if a.Text == "" {
		// Empty-text recent is a pure temporal BROWSE (no search query): fetch the
		// most-recently-updated nodes via GraphCaller and rerank by UpdatedAt. Every
		// other empty-text mode (text/default) still bails so precheck/deny owns the
		// requires-text message.
		if mode != "recent" {
			return false, kgtools.ToolResult{} // empty text → precheck/deny owns the message.
		}
		if err := accountQueryParams(armKnowledgeRecentBrowse, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		if gc == nil {
			return true, errorResult("recent browse: graph client unavailable")
		}
		return true, composeRecentBrowse(ctx, gc, a)
	}
	if err := accountQueryParams(armKnowledgeSearch, params.Arguments); err != nil {
		return true, errorResult(err.Error())
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
// plan is lowered into the server's store selection at decode
// (cmd/knowledge-server/internal/bootstrap/engine_decode.go), so only nodes of
// those types are ever returned — the same mechanism the plural-types browse arm
// uses (no client-side fetch-all).
// The read PAGES through the whole candidate set at BrowsePageSize per request,
// so every recency-eligible node is still considered before the sort while no
// single request is unbounded; the limit is honored only after ordering.
func composeRecentBrowse(ctx context.Context, gc GraphCaller, a queryArgs) kgtools.ToolResult {
	// Both type spellings map onto the fetch-level node-type set, with the SAME
	// precedence the text-bearing route applies: the plural set wins and the
	// singular is not applied when both are supplied, so the two arms cannot
	// disagree about the same payload. Reading only the plural spelling here
	// dropped the singular filter outright and paged the whole node set before
	// the temporal sort — wrong rows rather than an error.
	selection := &knowledgev1.Selection{}
	switch {
	case len(a.Types) > 0:
		selection.NodeTypes = a.Types
	case a.Type != "":
		selection.NodeTypes = []string{a.Type}
	}
	nodes, err := engine.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		plan := &knowledgev1.QueryPlan{
			Selection:         selection,
			IncludeTombstones: a.IncludeTombstones,
			Limit:             int32(engine.BrowsePageSize),
			// SET on every page including the first, where the value is empty:
			// presence is what selects the keyset browse.
			AfterId:   &cursor,
			SkipTotal: true,
		}
		resp, rerr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
			Target: domainTarget(a),
		})
		if rerr != nil {
			return nil, fmt.Errorf("fetch: %w", rerr)
		}
		page, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("decode: %w", derr)
		}
		return page, nil
	}, engine.BrowsePageSize)
	if err != nil {
		return errorResult("recent browse: " + err.Error())
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
// claimed text-search shapes and returns the composeKnowledgeSearch mode to use.
// The rule lives in segmentSearchClaimMode (search_mode_contract.go), shared
// with the custom-graph twin.
//
// hybrid and text are DIFFERENT arms, and the returned mode says which one ran:
// text is BM25 alone, while hybrid is the fused BM25-plus-vector path and is
// also what an absent mode resolves to. They were once collapsed onto a single
// value, which is exactly why a caller asking for text still paid for an
// embedding and still read a footer claiming a vector arm had contributed.
//
// An EXPLICIT mode claims unconditionally, so an id-selector alongside it does
// NOT turn the call into a lookup — the arm's param accounting classifies `id`
// REJECTED, so such a payload is refused by name rather than served as a search
// with the id silently dropped.
//
// A type/types/meta filter alongside text does NOT disqualify the claim. It used
// to, and the result was the worst of both worlds: the call fell through to a
// compiled plan whose text arm wins over its type arms, so the filter was
// dropped rather than applied and the retired knowledge server-search path
// returned nothing.
//
// Returns ("", false) for a by-id read (id / ids[]), which is a LOOKUP rather
// than a search, and for the filter-only browse shapes carrying no text
// (type-browse, meta-only) so compileQuery keeps owning those.
func knowledgeSearchModeFor(a queryArgs) (string, bool) {
	return segmentSearchClaimMode(a.Mode, a.Text != "", a.ID != "" || len(a.IDs) > 0)
}

// hasThoughtQueryFilter reports whether a query carries a thought-graph filter
// field — the recall/reflect surface owns those, not the knowledge search arm.
// Mirrors the engine hasThoughtFilter gate so the knowledge-search claim never
// swallows a recall-shaped query.
func hasThoughtQueryFilter(a queryArgs) bool {
	return a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != ""
}

// knowledgeQueryToSearchArgs builds the segmentSearchArgs JSON the
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
	// Both type spellings map onto the knowledge-search plural node-type
	// post-filter. The precedence MIRRORS the engine browse: the plural set wins
	// and the singular is not applied when both are supplied, so the search arm
	// and the browse arm cannot disagree about the same payload.
	switch {
	case len(a.Types) > 0:
		out["types"] = a.Types
	case a.Type != "":
		out["types"] = []string{a.Type}
	}
	if len(a.Meta) > 0 {
		out["meta"] = a.Meta
	}
	if halfLife > 0 {
		out["half_life"] = halfLife
	}
	// Gated on the resolved mode: a BM25-only search must not call the embedder
	// at all. Embedding and then discarding would still cost a round trip and,
	// on a metered embedder, still bill for it.
	if emb := deps.Embedder(); emb != nil && a.Text != "" && normalizeSegmentSearchMode(mode) != "text" {
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
// segmentSearchArgs (no `any` map) so the marshal is statically safe.
func params0RawForKnowledgeArgs(text string) json.RawMessage {
	raw, err := json.Marshal(segmentSearchArgs{Query: text})
	if err != nil {
		return json.RawMessage(`{"query":""}`)
	}
	return raw
}
