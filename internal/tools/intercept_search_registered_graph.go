// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_search_registered_graph.go is the client-side search-read claim for
// REGISTERED CUSTOM graph types (a GraphTypeDef whose name is not a builtin). A
// collected custom graph's BM25+HNSW segments are shipped CLIENT-side by the
// pipeline (segment Manager keyed on (gt, name)); the server RETURN_MODE_SEARCH
// path is retired and returns 0 hits for these graphs. Both the search tool and
// the query tool route a custom-graph text search through composeRegisteredGraphSearch
// → Manager.Search → RRF → ONE bulk hydrate, so the shipped segments are actually
// read instead of dispatching to the retired server search.

// composeRegisteredGraphSearch runs the ranked-search arm for a registered custom
// graph against the CLIENT segment engine — the (gt, name)-keyed mirror of
// composeResourceSearchClient (intercept_query_cloud_cicd.go), which hardcodes the
// account key, and composeKnowledgeSearch, which hardcodes knowledge/default. It
// (1) embeds the query client-side best-effort (nil embedder / empty query → the
// vector stays empty and Manager.Search degrades to the BM25 arm via
// RRF-over-one-list), (2) Manager.Search(gt, name, …) → RRF-fused hits, (3) ONE
// RETURN_MODE_NODES bulk hydrate keyed on the (gt, name) selector, (4) renders via
// the generic engine.RenderForCaller (custom graphs carry no resource-specific
// render kind). An un-collected / empty-name graph (no segments) renders zero
// results cleanly — graceful empty, NOT an error (Manager.Search tolerates an
// empty instance key the same way the cloud arm tolerates an empty account).
// During the bind-first wiring window (bind-first startup) the segment Manager is not yet
// wired; the function gates on PipelineReady at its top and returns a not-ready
// error before any deref. No server RETURN_MODE_SEARCH fallback exists — it is
// never dispatched.
func composeRegisteredGraphSearch(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, gt kgtypes.GraphType, name, query, format string) kgtools.ToolResult {
	// Readiness gate (bind-first startup): the mgr==nil case below is already nil-safe (no
	// panic) but emits a permanent-degrade message that misleads during the
	// bind-first wiring window. Add the uniform not-ready pre-check so the window
	// is distinguishable from a genuinely-unwired pipeline. Both entry points (the
	// search tool and the query tool) funnel through here.
	if !deps.PipelineReady() {
		return errorResult(string(gt) + " search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	if mgr == nil {
		return errorResult(string(gt) + " search: client segment engine unavailable")
	}
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil && query != "" {
		if vec, err := emb.EmbedBinary(ctx, query); err == nil && len(vec) > 0 {
			queryVec = vec
		}
	}
	hits, err := mgr.Search(ctx, gt, name, query, queryVec, knowledgeSearchDefaultLimit)
	if err != nil {
		return errorResult(string(gt) + " search: client engine: " + err.Error())
	}
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), hydrateSelector{Graph: string(gt), Name: name}, hits)
	if err != nil {
		return errorResult(string(gt) + " search: hydrate: " + err.Error())
	}
	if format == "" {
		format = "text"
	}
	return engine.RenderForCaller(query, results, format, nil, "")
}

// InterceptQueryRegisteredGraphSearch claims the QUERY-tool text-search shapes for
// a registered custom graph that would otherwise compile to a server
// RETURN_MODE_SEARCH dispatch (the retired path) and routes them through the
// CLIENT segment engine via composeRegisteredGraphSearch. It is the query-tool
// sibling of InterceptQueryKnowledgeSearch (intercept_search_knowledge.go), gated
// on a NON-builtin graph instead of knowledge/default.
//
// Self-gates so a call it does not own falls through to the next member of
// runQueryDomainIntercepts (and ultimately tools.InterceptQuery): it claims ONLY
// query(graph=<custom>, mode∈{text,recent,default-text}, text:<non-empty>) where
// the shape is not a recall/reflect thought query and not an id/ids/type/meta
// browse. A builtin graph, an empty text, or a non-search shape returns (false,_).
func InterceptQueryRegisteredGraphSearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// Only a registered custom graph: empty/builtin graphs are owned by the
	// knowledge / cloud / cicd / practice / code arms upstream.
	if a.Graph == "" || kgtypes.IsBuiltinGraphType(a.Graph) {
		return false, kgtools.ToolResult{}
	}
	if hasThoughtQueryFilter(a) {
		return false, kgtools.ToolResult{} // recall/reflect shape stays on the thoughts surface.
	}
	if a.Text == "" {
		return false, kgtools.ToolResult{} // empty text → precheck/deny owns the message.
	}
	// Claim only the text-search shapes for a custom graph: mode=hybrid / text /
	// recent, or the DEFAULT mode (empty) carrying ONLY a text query — no
	// id/ids/type/meta browse signal (those non-search shapes stay on the
	// compileQuery browse/getNode path). Distinct from knowledgeSearchModeFor,
	// which excludes "hybrid" because the knowledge arm lets default/hybrid fall to
	// InterceptQuery's engine path; a custom graph must NOT fall there (it would hit
	// the retired server RETURN_MODE_SEARCH), so the custom arm claims hybrid here.
	if !registeredGraphSearchShape(a) {
		return false, kgtools.ToolResult{}
	}
	return true, composeRegisteredGraphSearch(ctx, deps, deps.SegmentManager(),
		kgtypes.GraphType(a.Graph), a.Name, a.Text, a.Format)
}

// registeredGraphSearchShape reports whether a custom-graph query (already gated on
// non-builtin graph + non-empty Text) is a claimed text-search shape. Claims
// mode∈{hybrid, text, recent} and the default mode (empty) carrying ONLY a text
// query — any id/ids/type/meta signal means a browse/getNode read, which stays on
// the compileQuery path. The hybrid arm is the difference from
// knowledgeSearchModeFor: the knowledge arm lets default/hybrid fall through to
// InterceptQuery's engine path, but a custom graph must be claimed here so it never
// reaches the retired server search.
func registeredGraphSearchShape(a queryArgs) bool {
	switch a.Mode {
	case "hybrid", "text", "recent":
		return true
	case "":
		return a.ID == "" && len(a.IDs) == 0 && a.Type == "" && len(a.Meta) == 0
	default:
		return false
	}
}
