// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// InterceptSearch is the cmd/knowledge client's "search" interceptor.
//
// Responsibilities, layered:
//
//  0. mode:"similar" claim. A knowledge/default search carrying mode=similar +
//     node_id resolves that node's STORED vector from the client-local HNSW
//     segments and returns its nearest neighbors (composeSimilarNodeSearch),
//     self-excluded. Claimed after the logs/code short-circuits, before the
//     reducible-graph arms; loud-errors rather than falling through.
//  1. graph=logs short-circuit. Log graph search runs entirely
//     client-side: searchLogs (tools_logs_search.go) issues
//     gc.Call("query", graph:"logs", text:..., name:..., format:"json")
//     and renders templates locally. The server-side search.go dispatch
//     returns errLogsHandledClientSide for graph=logs; the rerank +
//     embed branches below do not apply since log graphs carry no
//     vector index.
//  2. Client-side query embedding (Phase 4.5). When deps.Embedder() is
//     non-nil, the caller did not already supply query_vector, AND the
//     resolved mode is not BM25-only, the query text is embedded locally and
//     the bytes are forwarded via the query_vector wire field. The server's
//     compositor short-circuits its own embed call, so servers without a
//     Voyage key still return vector-quality results.
//  3. Client-side rerank. When the resolved [reranker] axis has a
//     credential (or a keyless base_url) AND the resolved mode is not
//     BM25-only, this interceptor widens limit + coerces format=json on the
//     wire, calls the server, hydrates the JSON response, invokes the
//     configured reranker locally through the rerank registry, and
//     re-renders for the caller.
//  4. Mode honoring. The declared `mode` selects which retrieval arms run.
//     mode:text suppresses BOTH pre-steps above and refuses a payload that
//     also asks for a vector operation; search_mode_contract.go holds the
//     vocabulary and the reasoning.
//
// Returns (handled, result). When handled is false, the next interceptor
// (or the bare server call) takes over with the ORIGINAL params.
//
// This outer is deliberately thin. It owns exactly one thing the arms cannot:
// the CALLER-BOUNDARY limit clamp and its disclosure. Placing it here rather
// than in the arms is what makes the declared maximum bind on EVERY serving
// path — rerank success, all four rerank degrade branches, keyless installs,
// the custom-graph arm, logs, code and similar — without enumerating them.
// It also keeps the clamp strictly BEFORE the rerank rewrite widens the same
// wire key to the candidate-pool size; see clampSearchCallerLimit for why those
// two writes must not be collapsed.
func InterceptSearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if claimed, res, done := searchClaimGate(params); done {
		return claimed, res
	}
	var clamped bool
	params.Arguments, clamped = clampSearchCallerLimit(params.Arguments)
	handled, res := interceptSearchArms(ctx, deps, params)
	if handled && clamped && !res.IsError {
		// A SEPARATE content block, never concatenated: a format:json body must
		// stay independently parseable.
		res.Content = append(res.Content, kgtools.ContentBlock{Type: "text", Text: searchLimitClampNotice})
	}
	return handled, res
}

// interceptSearchArms is InterceptSearch's body: every per-graph claim, the
// mode resolution, and the embed/rerank pipeline. Split out so the clamp and
// its disclosure in the outer apply uniformly to whatever this returns.
func interceptSearchArms(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	// graph=logs short-circuit. Decoded here so the rewrite/
	// embed/rerank pipeline below never sees a log-graph payload it
	// wasn't designed for.
	var sniff searchArgs
	if err := json.Unmarshal(params.Arguments, &sniff); err == nil && sniff.Graph == "logs" {
		h := &Handler{Deps: deps}
		return true, h.searchLogs(ctx, sniff)
	}
	// graph=code claim. Code search runs entirely client-side
	// via the SAME composeCodeSearch composer InterceptQueryCodeSearch uses; the
	// engine search-code path is denied (compileSearch isCodeGraph), so this is
	// the only claim for the code arm. It owns its OWN query embedding: interceptSearchCode
	// auto-embeds the query set client-side (maybeEmbedCodeQueries) and threads the
	// per-query vectors into each code-search QueryPlan's QueryVecs, so it does NOT
	// fall through to the generic maybeEmbedQuery/rewrite/rerank branches below
	// (those target the knowledge vector index, not the code graph). A caller-supplied
	// query_vector on a graph=code search is honored too (broadcast to queries[0]).
	// The Query→Text mapping + mergeCodeQueries mirror InterceptQueryCodeSearch.
	if sniff.Graph == "code" {
		gc := deps.GraphCaller()
		if gc == nil {
			return true, errorResult("code search: graph client unavailable")
		}
		if handled, res := interceptSearchCode(ctx, deps, gc.Execute, params.Arguments); handled {
			return true, res
		}
	}
	// mode:"similar" claim. A knowledge/default search carrying mode=similar +
	// node_id resolves that node's STORED vector from the client-local HNSW
	// segments and returns its nearest neighbors (interceptSearchSimilar →
	// composeSimilarNodeSearch) — NO server search, NO fresh query-text embed.
	// Placed AFTER the logs/code short-circuits and BEFORE
	// interceptSearchReducibleGraph so a normal (empty-mode) search flows past it.
	if handled, res := interceptSearchSimilar(ctx, deps, sniff); handled {
		return true, res
	}
	// For completeness: the SEARCH tool is a SEPARATE client
	// compile path from the query tool. engine.compileSearch is reducible for
	// practice/cloud/cicd/linkage/web/pdf/checks and would dispatch
	// RETURN_MODE_SEARCH to the server. Claim each of those reducible graphs there
	// (intercept_search_reducible_graph.go) — practice/cloud/cicd served by the
	// client segment engine, web/pdf by the client-computed BM25 read over the
	// drained raw graph, checks by its own served arm, and linkage refused by name
	// because it carries no ranked index — so the SEARCH tool emits no RETURN_MODE_SEARCH
	// for ANY reducible graph. Only the knowledge/default arm (and graph=logs/code
	// above) flows past this point.
	if handled, res := interceptSearchReducibleGraph(ctx, deps, sniff.Graph, params.Arguments); handled {
		return true, res
	}
	// Fold a `queries` array into the single `query` for the non-code arms (the
	// code arm above folds its own via mergeCodeQueries). The knowledge client
	// engine + the server search dispatch read only `query`; a `queries`-only
	// payload would otherwise embed + search the empty string. Multiple queries
	// join into one text query (BM25 ORs the terms; the vector embeds the
	// combined text) — the code arm is the only one that fans out per-query.
	params.Arguments = normalizeQueriesToQuery(params.Arguments)

	// Resolve the declared mode ONCE. bm25Only is what suppresses both the embed
	// and the rerank below; the contract file owns what each mode means.
	execMode := normalizeSegmentSearchMode(sniff.Mode)
	bm25Only := execMode == "text"
	if bm25Only {
		// Refuse BEFORE any rewrite or embed: a payload asking for BM25-only
		// retrieval and a vector operation at once cannot be honored both ways.
		if msg := searchModeConflict(params.Arguments); msg != "" {
			return true, errorResult(msg)
		}
	}

	// The rerank decision, including the caller's explicit rerank:false opt-out
	// and the mode suppression. Extracted as a pure predicate so the credential
	// presence is an argument a test can choose rather than an ambient value it
	// inherits. The credential is resolved from the [reranker] axis's OWN
	// provider, so an operator reranking on a different provider than they
	// embed with is read correctly.
	rerankReady := rerankCredentialPresent()
	hasReranker := searchRerankActive(rerankReady, searchRerankParam(params.Arguments), bm25Only)
	expanded, saved, hasRewrite, err := rewriteSearchArgs(params.Arguments, hasReranker)
	if err != nil {
		return true, errorResult("rewrite search args: " + err.Error())
	}
	slog.Debug("rerank-trace: InterceptSearch gate",
		"graph", sniff.Graph, "rerank_credential", rerankReady,
		"has_reranker", hasReranker, "has_rewrite", hasRewrite)

	// Embed query text client-side when an embedder is wired and the
	// caller did not already supply query_vector. embedded carries the
	// (possibly mutated) args; whether mutated or not, embeddedDone
	// indicates the interceptor must own the wire call from here.
	args := expanded
	if !hasRewrite {
		args = params.Arguments
	}
	// Suppressed entirely under a BM25-only mode: not called, rather than called
	// and discarded. On a metered embedder the difference is billed.
	embedded, didEmbed, embedErr := embedKnowledgeQuery(ctx, deps, args, bm25Only)
	if embedErr != nil {
		return true, errorResult(embedErr.Error())
	}
	slog.Debug("rerank-trace: post-embed",
		"did_embed", didEmbed, "bm25_only", bm25Only, "embedder_nil", deps.Embedder() == nil)
	// The knowledge/default arm is CLAIMED by the client engine
	// UNCONDITIONALLY — even with no rewrite and no client embed (BM25-only via
	// RRF-over-one-list). The segment Manager is wired for the life of the daemon
	// EXCEPT during the bind-first wiring window (bind-first startup), which the PipelineReady
	// gate below rejects with a not-ready error before any deref. Only fall through
	// to the bare server search when this is NOT the knowledge/default arm. Other
	// graphs keep the pre-existing fall-through contract (the SEARCH-tool
	// practice/cloud/cicd claims are added separately).
	claimKnowledge := isKnowledgeDefaultGraph(sniff.Graph)
	if !hasRewrite && !didEmbed && !claimKnowledge {
		slog.Debug("rerank-trace: fall-through to bare search (no rewrite, no embed)")
		return false, kgtools.ToolResult{}
	}
	args = embedded

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("server unreachable; start it with `knowledge-server`")
	}
	// The knowledge-arm readiness gate (bind-first startup) lives inside
	// runKnowledgeOrServerSearch — the single chokepoint before composeKnowledgeSearch
	// dereferences the segment Manager — so it does not add to this function's
	// statement budget. Other graphs ride the server Execute path and are unaffected.
	//
	// Route the tail through the compile-or-DENY dispatcher: a reducible search
	// — the Router surfaces ErrNoBackend on a missing backend, which
	// renderEngineError translates into an actionable install-or-login message,
	// so the pre-flight Healthy() probe (always-local in the prior design) is unnecessary.
	// compiles to Engine.Execute; an unrecognized shape is denied legibly (there is
	// no wire fallback). The per-graph search intercepts (code /
	// cloud / cicd / practice) claim the specialized shapes upstream, so only the
	// reducible knowledge/default search reaches here. The embed / rerank pre-steps
	// above are preserved verbatim; applyClientRerank below still hydrates the json
	// envelope (the engine search render in json mode emits the same
	// SearchJSONResponse shape via renderJSON).
	resp, derr := runKnowledgeOrServerSearch(ctx, deps, gc, sniff.Graph, args)
	if derr != nil {
		return true, errorResult("search call failed: " + derr.Error())
	}
	if !hasRewrite || !hasReranker {
		slog.Debug("rerank-trace: skipping rerank stage",
			"has_rewrite", hasRewrite, "has_reranker", hasReranker)
		return true, resp
	}
	slog.Debug("rerank-trace: invoking applyClientRerank",
		"pool_size", widePoolSize, "top_k", widePoolTopK)
	reranker := buildReranker(ctx, widePoolSize, widePoolTopK)
	return true, applyClientRerank(ctx, resp, saved, reranker)
}

// searchClaimGate settles the two questions that precede any search work: does
// this call belong to the search tool at all, and does its payload carry a param
// the tool does not declare. done=true means InterceptSearch returns (claimed,
// res) immediately — not ours, or ours and refused.
//
// It is a separate function only so the param-accounting guard does not push
// InterceptSearch past the statement cap; InterceptSearch's body is otherwise
// unchanged.
func searchClaimGate(params kgtools.CallToolParams) (claimed bool, res kgtools.ToolResult, done bool) {
	if params.Name != "search" {
		return false, kgtools.ToolResult{}, true
	}
	if err := rejectUndeclaredParams("search", "", SearchToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error()), true
	}
	return false, kgtools.ToolResult{}, false
}

// normalizeQueriesToQuery folds a `queries` array into the single `query` field
// when `query` is empty, preserving every other arg. The non-code search arms
// (the knowledge client engine + the server dispatch) read only `query`, so a
// `queries`-only payload would otherwise embed + search the empty string.
// Multiple queries join with spaces into one text query. Returns raw unchanged
// when there's nothing to fold or on any decode error (fail-open).
func normalizeQueriesToQuery(raw json.RawMessage) json.RawMessage {
	var probe struct {
		Query   string   `json:"query"`
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return raw
	}
	if probe.Query != "" || len(probe.Queries) == 0 {
		return raw
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	joined, err := json.Marshal(strings.Join(probe.Queries, " "))
	if err != nil {
		return raw
	}
	m["query"] = joined
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// interceptSearchCode claims search(graph:code) and routes it through the
// shared composeCodeSearch composer. It decodes the code-search args
// (repo/repos/branch/path_prefix/group_by_file/...) from the search payload and
// maps the search tool's `query` (+ `queries`) into the code composer's query
// list via mergeCodeQueries — the SAME merge InterceptQueryCodeSearch does.
// Returns (false,_) when there is no query (not the search shape) so the caller
// falls through. The exec seam is a parameter so the claim is unit-testable
// against a fake Execute.
func interceptSearchCode(ctx context.Context, deps ClientDeps, exec engine.ExecuteFn, raw json.RawMessage) (bool, kgtools.ToolResult) {
	var a codeSearchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// The search tool carries the query under `query`; codeSearchArgs.Text is
	// json:"text". Decode the search-shaped query field and fold it in.
	var sa struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(raw, &sa)
	queries := mergeCodeQueries(sa.Query, mergeCodeQueries(a.Text, a.Queries))
	if len(queries) == 0 {
		return false, kgtools.ToolResult{} // no query → fall through.
	}
	queryVecs := buildCodeQueryVecs(maybeEmbedCodeQueries(ctx, codeEmbedder(deps), queries), a.QueryVector)
	return true, composeCodeSearch(ctx, deps, exec, a, queries, queryVecs)
}

// interceptSearchSimilar claims search(mode:similar) for the knowledge/default
// graph: it resolves the named node's STORED vector from the client-local HNSW
// segments and returns its nearest neighbors via composeSimilarNodeSearch (NO
// server search, NO fresh embed). Returns (false,_) — flow past to the other arms —
// when this is not a knowledge/default mode=similar search. When it IS, it is
// CLAIMED unconditionally and loud-errors (never falls through) on: empty node_id,
// any nil dep (GraphCaller / SegmentManager / SegmentVectorResolver), and an absent
// stored vector (handled downstream in composeSimilarNodeSearch). similar over
// code/cloud is out of scope — the stored-vector resolver targets GraphKnowledge.
func interceptSearchSimilar(ctx context.Context, deps ClientDeps, sniff searchArgs) (bool, kgtools.ToolResult) {
	if sniff.Mode != "similar" || !isKnowledgeDefaultGraph(sniff.Graph) {
		return false, kgtools.ToolResult{}
	}
	if sniff.NodeID == "" {
		return true, errorResult("similar search: node_id is required for mode:similar")
	}
	// Readiness gate (bind-first startup): mode:similar resolves the stored vector through the
	// pipeline-backed SegmentVectorResolver; during the bind-first wiring window
	// that handle is nil. Distinguish the transient window from a permanently
	// unwired pipeline so a retry succeeds. Plain BM25 search does not reach here.
	if !deps.PipelineReady() {
		return true, errorResult("similar search: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	gc := deps.GraphCaller()
	mgr := deps.SegmentManager()
	res := deps.SegmentVectorResolver()
	if gc == nil || mgr == nil || res == nil {
		return true, errorResult("similar search: client segment engine unavailable (the local pipeline is not wired)")
	}
	k := int(sniff.Limit)
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	return true, composeSimilarNodeSearch(ctx, gc, mgr, res, sniff.NodeID, k, sniff.Format, sniff.Fields)
}

// maybeEmbedQuery decodes args into a generic map, embeds the "query"
// text via emb when (a) emb is non-nil, (b) the args carry a non-empty
// query string, and (c) query_vector is not already set, then re-encodes
// args with the new query_vector field. Returns (originalArgs, false)
// when any precondition fails — caller falls through to the original
// args path. Returns (newArgs, true) when a query_vector was injected.
//
// Wire-shape note: query_vector is base64-encoded bytes on the wire (the
// std JSON []byte codec handles base64 transparently); we set it as
// raw []byte and let json.Marshal do the encoding.
func maybeEmbedQuery(ctx context.Context, emb embed.BinaryEmbedder, args json.RawMessage) (json.RawMessage, bool) {
	if emb == nil {
		return args, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return args, false
	}
	if _, alreadySet := obj["query_vector"]; alreadySet {
		return args, false
	}
	var query string
	if raw, ok := obj["query"]; ok {
		if err := json.Unmarshal(raw, &query); err != nil {
			return args, false
		}
	}
	if query == "" {
		return args, false
	}
	vec, err := emb.EmbedBinary(ctx, query)
	if err != nil || len(vec) == 0 {
		return args, false
	}
	enc, err := json.Marshal(vec)
	if err != nil {
		return args, false
	}
	obj["query_vector"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return args, false
	}
	return out, true
}
