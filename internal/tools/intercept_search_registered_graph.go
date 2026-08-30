// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// intercept_search_registered_graph.go is the client-side search-read claim for
// REGISTERED CUSTOM graph types (a GraphTypeDef whose name is not a builtin). A
// collected custom graph's BM25+HNSW segments are shipped CLIENT-side by the
// pipeline (segment Manager keyed on (gt, name)); the server RETURN_MODE_SEARCH
// path is retired and returns 0 hits for these graphs. Both the search tool and
// the query tool route a custom-graph text search through composeRegisteredGraphSearch
// → Manager.Search → RRF → ONE bulk hydrate → the shared post-hydrate tail
// (finishSegmentSearchRender, intercept_search_knowledge.go), so the shipped
// segments are actually read instead of dispatching to the retired server search
// AND the ranked rows go through the same filtering/rerank/projection stage the
// knowledge arm applies.

// composeRegisteredGraphSearch runs the ranked-search arm for a registered custom
// graph against the CLIENT segment engine — the (gt, name)-keyed mirror of
// composeResourceSearchClient (intercept_query_cloud_cicd.go), which hardcodes the
// account key, and composeKnowledgeSearch, which hardcodes knowledge/default. It
// (1) embeds the query client-side best-effort (nil embedder / empty query → the
// vector stays empty and Manager.Search degrades to the BM25 arm via
// RRF-over-one-list), (2) Manager.Search(gt, name, …) at the caller's limit →
// RRF-fused hits, (3) ONE RETURN_MODE_NODES bulk hydrate keyed on the (gt, name)
// selector, (4) finishes through finishSegmentSearchRender — the tail it SHARES
// with composeKnowledgeSearch, which applies the type/types and metadata
// post-filters, the mode=recent temporal rerank, the fields projection and the
// search-mode footer. Sharing that tail is what makes this arm a twin of the
// knowledge arm rather than a second implementation free to drift.
//
// THE SELECTOR IS VALIDATED BEFORE ANY OF THAT (validateRegisteredGraphSelector,
// registered_graph_selector.go): an unregistered graph type and a registered type
// whose named graph has never been collected are both ERRORS here, carrying the
// same two classifications the server's resolveRegisteredCustom draws. Validating
// in this composer rather than in the two claim gates is deliberate — the search
// tool and the query tool both funnel through here, so the rule has one
// derivation and the two tools cannot drift about which selectors are real.
// During the bind-first wiring window (bind-first startup) the segment Manager is not yet
// wired; the function gates on PipelineReady at its top and returns a not-ready
// error before any deref. No server RETURN_MODE_SEARCH fallback exists — it is
// never dispatched.
func composeRegisteredGraphSearch(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, gt kgtypes.GraphType, name string, a segmentSearchArgs) kgtools.ToolResult {
	// Selector gate. Placed ahead of the embed below so an unresolvable selector
	// never bills an embedding, and ahead of Manager.Search so a graph that does
	// not exist can never render as a graph that matched nothing. The message is
	// returned as-is: it already names the graph.
	if res, notReady := segmentSearchNotReady(deps, mgr, gt); notReady {
		return res
	}
	if err := validateRegisteredGraphSelector(ctx, deps, gt, name); err != nil {
		return errorResult(err.Error())
	}
	return composeSegmentGraphSearch(ctx, deps, mgr, gt, name, a)
}

// composeSegmentGraphSearch is the ranked-search body with NO selector gate of its
// own — everything composeRegisteredGraphSearch does after validating a registered
// selector: embed-if-the-mode-wants-one, Manager.Search, one bulk hydrate, and the
// shared render tail.
//
// IT IS SPLIT OUT FOR A SECOND CALLER WHOSE SELECTOR RULE IS DIFFERENT. The checks
// graph is a BUILTIN SINGLETON: validateRegisteredGraphSelector rejects it twice
// over — it admits only registered custom types, and it then requires an instance
// name a singleton addressing none can never supply. That arm applies the
// singleton rule directly and reaches the ranked body here, so both arms share ONE
// derivation of what a ranked search over shipped segments is, rather than the
// second one being a copy free to drift.
//
// THE ENGINE KEY AND THE WIRE SELECTOR ARE TWO DIFFERENT NAMES, resolved
// separately below, and collapsing them is a defect this repo has now paid for
// three times. `name` is what the CALLER may legally put on a selector; the
// segment engine is keyed by the name this process seals segments under. They
// agree for every family that carries a real instance field and DIVERGE for a
// singleton whose selector policy admits no name: the engine holds it under the
// canonical instance while the wire must still be sent an empty one. A single
// variable serving both addressed an engine instance nothing had written to and
// returned a confident zero.
func composeSegmentGraphSearch(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, gt kgtypes.GraphType, name string, a segmentSearchArgs) kgtools.ToolResult {
	if res, notReady := segmentSearchNotReady(deps, mgr, gt); notReady {
		return res
	}
	// The INTERNAL key. Identity for every family with a real instance field, so
	// this is safe on the shared path and changes nothing for custom graphs.
	engineName := workingset.CanonicalInstanceName(gt, name)
	mode := normalizeSegmentSearchMode(a.Mode)
	// The embed is GATED on the mode rather than issued and discarded: a BM25-only
	// search that still pays for an embedding is the cost this contract removes,
	// and on a metered embedder the difference is billed.
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil && a.Query != "" && mode != "text" {
		if vec, err := emb.EmbedBinary(ctx, a.Query); err == nil && len(vec) > 0 {
			queryVec = vec
		}
	}
	engineText, engineVec := segmentSearchEngineArms(mode, a.Query, queryVec)
	if mode == "vector" && len(engineVec) == 0 {
		return errorResult(string(gt) + " search: mode:vector needs a query embedding, " +
			"but no embedder is configured — use mode:hybrid or mode:text instead")
	}
	// Honor the caller's limit with the same <=0 default the knowledge arm uses
	// (intercept_search_knowledge.go). Boundedness is unchanged: the hydrate below
	// is the same ids[] RETURN_MODE_NODES read, which the server ceiling clamps.
	k := a.Limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	hits, err := mgr.Search(ctx, gt, engineName, engineText, engineVec, k)
	if err != nil {
		return errorResult(string(gt) + " search: client engine: " + err.Error())
	}
	// THE WIRE SELECTOR KEEPS THE CALLER'S NAME, not the engine key. hydrateEngineHits
	// marshals Name straight into the query args, and a singleton's server-side
	// selector policy REJECTS a set name — so sending the canonical instance here
	// would trade a silent zero for a refused hydrate.
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), hydrateSelector{Graph: string(gt), Name: name}, hits)
	if err != nil {
		return errorResult(string(gt) + " search: hydrate: " + err.Error())
	}
	return finishSegmentSearchRender(a.Query, results, a, mode, engineText, engineVec)
}

// InterceptQueryRegisteredGraphSearch claims the QUERY-tool text-search shapes for
// a registered custom graph that would otherwise compile to a server
// RETURN_MODE_SEARCH dispatch (the retired path) and routes them through the
// CLIENT segment engine via composeRegisteredGraphSearch. It is the query-tool
// sibling of InterceptQueryKnowledgeSearch (intercept_search_knowledge.go), gated
// on a NON-builtin graph instead of knowledge/default.
//
// Self-gates so a call it does not own falls through to the next member of
// runQueryDomainIntercepts (and ultimately tools.InterceptQuery): a builtin
// graph, an empty text, a recall/reflect thought shape, or a shape
// registeredGraphSearchShape declines all return (false,_). That predicate is
// where the claimed set is defined; it is not restated here.
func InterceptQueryRegisteredGraphSearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// Only a custom graph: empty/builtin graphs are owned by the knowledge /
	// cloud / cicd / practice / code arms upstream. This gate is SHAPE, not
	// registration — an unregistered string is claimed here so that
	// composeRegisteredGraphSearch can refuse it by name instead of letting it
	// fall through to a dispatch that renders an indistinguishable zero.
	if a.Graph == "" || kgtypes.IsBuiltinGraphType(a.Graph) {
		return false, kgtools.ToolResult{}
	}
	if hasThoughtQueryFilter(a) {
		return false, kgtools.ToolResult{} // recall/reflect shape stays on the thoughts surface.
	}
	if a.Text == "" {
		return false, kgtools.ToolResult{} // empty text → precheck/deny owns the message.
	}
	// registeredGraphSearchShape is the authoritative definition of which shapes
	// this arm claims — read it there, not here. Deliberately NOT paraphrased: a
	// restatement of a routing gate is a second derivation of the rule, and it
	// rots the moment the predicate moves. The enumeration that used to sit here
	// did exactly that, outliving the browse-signal exclusion it described.
	_, claimed := registeredGraphSearchShape(a)
	if !claimed {
		return false, kgtools.ToolResult{}
	}
	if err := accountQueryParams(armRegisteredGraphSearch, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	return true, composeRegisteredGraphSearch(ctx, deps, deps.SegmentManager(),
		kgtypes.GraphType(a.Graph), a.Name, registeredGraphQueryToSearchArgs(a))
}

// registeredGraphSearchShape reports whether a custom-graph query (already gated
// on non-builtin graph + non-empty Text) is a claimed text-search shape, and
// returns the mode composeRegisteredGraphSearch runs it under. The rule itself
// lives in segmentSearchClaimMode (search_mode_contract.go), shared with the
// knowledge twin so the two arms cannot drift about which shapes are a search.
//
// ONE intentional difference from the knowledge twin remains: that arm ALSO
// serves BARE mode:recent (empty text) as a temporal browse via
// composeRecentBrowse. This arm declines it — a custom graph has no browse-side
// counterpart — which is why the empty-text bail upstream runs before this
// predicate is ever reached.
func registeredGraphSearchShape(a queryArgs) (string, bool) {
	return segmentSearchClaimMode(a.Mode, a.Text != "", a.ID != "" || len(a.IDs) > 0)
}

// registeredGraphQueryToSearchArgs builds the segmentSearchArgs the custom-graph
// query arm consumes from a query-tool queryArgs. It mirrors
// knowledgeQueryToSearchArgs (intercept_query_knowledge_search.go) field for
// field, with two deliberate differences: it returns the STRUCT rather than
// json.RawMessage, because this arm calls composeRegisteredGraphSearch directly
// instead of round-tripping through a JSON payload; and it does NOT embed,
// because composeRegisteredGraphSearch already embeds and duplicating that would
// issue two embed calls per request.
//
// The mode comes from registeredGraphSearchShape rather than from a.Mode so the
// claimed set and the executed mode can never disagree — the predicate is pure
// and the caller has already established that it claims.
func registeredGraphQueryToSearchArgs(a queryArgs) segmentSearchArgs {
	mode, _ := registeredGraphSearchShape(a)
	out := segmentSearchArgs{
		Query:  a.Text,
		Limit:  int(a.Limit),
		Format: a.Format,
		Fields: a.Fields,
		Mode:   mode,
	}
	// Both type spellings map onto the plural node-type post-filter, with the
	// precedence copied from the knowledge builder: the plural set wins and the
	// singular is NOT also applied, so the two arms cannot disagree about one
	// payload.
	switch {
	case len(a.Types) > 0:
		out.Types = a.Types
	case a.Type != "":
		out.Types = []string{a.Type}
	}
	if len(a.Meta) > 0 {
		out.Meta = a.Meta
	}
	if mode == "recent" {
		out.HalfLife = recentTemporalHalfLifeDays
	}
	return out
}

// segmentSearchNotReady is the shared readiness gate every segment-engine arm runs
// FIRST, ahead of its own selector rule.
//
// ORDER IS THE WHOLE POINT AND IT IS BEHAVIORAL. The mgr==nil case below is
// nil-safe on its own, but it emits a permanent-degrade message that misleads
// during the bind-first wiring window; and a selector gate that ran first would
// answer a starting daemon with a registry-unavailable complaint about the
// SELECTOR, which sends the caller to fix a call that was fine. It is a shared
// helper rather than two copies because both arms must give the same answer to
// the same condition.
func segmentSearchNotReady(deps ClientDeps, mgr SegmentSearcher, gt kgtypes.GraphType) (kgtools.ToolResult, bool) {
	if !deps.PipelineReady() {
		return errorResult(string(gt) + " search: daemon still starting — LLM pipeline not ready yet, retry shortly"), true
	}
	if mgr == nil {
		return errorResult(string(gt) + " search: client segment engine unavailable"), true
	}
	return kgtools.ToolResult{}, false
}
