// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
)

// InterceptSearch is the cmd/knowledge stdio client's "search" interceptor.
//
// Three responsibilities, layered:
//
//  1. graph=logs short-circuit (BCN11.2). Log graph search runs entirely
//     client-side: searchLogs (tools_logs_search.go) issues
//     gc.Call("query", graph:"logs", text:..., name:..., format:"json")
//     and renders templates locally. The server-side search.go dispatch
//     returns errLogsHandledClientSide for graph=logs; the rerank +
//     embed branches below do not apply since log graphs carry no
//     vector index.
//  2. Client-side query embedding (Phase 4.5). When deps.Embedder() is
//     non-nil and the caller did not already supply query_vector, the
//     query text is embedded locally and the bytes are forwarded via
//     the query_vector wire field. The server's compositor short-circuits
//     its own embed call, so post-BCN5 servers (no Voyage key) still
//     return vector-quality results.
//  3. Client-side rerank (post-BCN6 R2). When [credentials] voyage_api_key
//     is configured this interceptor widens limit + coerces format=json
//     on the wire, calls the server, hydrates the JSON response, invokes
//     the moved cmd/knowledge/internal/rerank package's Voyage reranker
//     locally, and re-renders for the caller.
//
// Returns (handled, result). When handled is false, the next interceptor
// (or the bare server call) takes over with the ORIGINAL params.
func InterceptSearch(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "search" {
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	// graph=logs short-circuit (BCN11.2). Decoded here so the rewrite/
	// embed/rerank pipeline below never sees a log-graph payload it
	// wasn't designed for.
	var sniff searchArgs
	if err := json.Unmarshal(params.Arguments, &sniff); err == nil && sniff.Graph == "logs" {
		h := &Handler{Deps: deps}
		return true, h.searchLogs(ctx, sniff)
	}
	// graph=code claim (T-GTB6 CLASS-A). Code search runs entirely client-side
	// via the SAME composeCodeSearch composer InterceptQueryCodeSearch uses; the
	// engine search-code path is denied (compileSearch isCodeGraph), so this is
	// the only claim for the code arm. Fires BEFORE the embed/rewrite/rerank
	// branches (those target the knowledge vector index, which code search does
	// not use). The Query→Text mapping + mergeCodeQueries mirror
	// InterceptQueryCodeSearch.
	if sniff.Graph == "code" {
		gc := deps.GraphCaller()
		if gc == nil {
			return true, errorResult("code search: graph client unavailable")
		}
		if handled, res := interceptSearchCode(ctx, deps, gc.Execute, params.Arguments); handled {
			return true, res
		}
	}
	voyageKey := config.VoyageAPIKey()
	hasReranker := voyageKey != ""
	// Honor an explicit rerank:false — the caller opts out of the
	// (latency-heavy) client-side Voyage rerank; unset or rerank:true keep
	// the key-driven default. Without this the rerank param was captured into
	// savedState.originalRerank but never consulted, so rerank:false was
	// silently ignored and every search paid the rerank round-trip.
	if hasReranker {
		var rr struct {
			Rerank *bool `json:"rerank"`
		}
		if err := json.Unmarshal(params.Arguments, &rr); err == nil && rr.Rerank != nil && !*rr.Rerank {
			hasReranker = false
		}
	}
	expanded, saved, hasRewrite, err := rewriteSearchArgs(params.Arguments, hasReranker)
	if err != nil {
		return true, errorResult("rewrite search args: " + err.Error())
	}
	slog.Debug("rerank-trace: InterceptSearch gate",
		"graph", sniff.Graph, "voyage_key_len", len(voyageKey),
		"has_reranker", hasReranker, "has_rewrite", hasRewrite)

	// Embed query text client-side when an embedder is wired and the
	// caller did not already supply query_vector. embedded carries the
	// (possibly mutated) args; whether mutated or not, embeddedDone
	// indicates the interceptor must own the wire call from here.
	args := expanded
	if !hasRewrite {
		args = params.Arguments
	}
	embedded, didEmbed := maybeEmbedQuery(ctx, deps.Embedder(), args)
	slog.Debug("rerank-trace: post-embed",
		"did_embed", didEmbed, "embedder_nil", deps.Embedder() == nil)
	if !hasRewrite && !didEmbed {
		slog.Debug("rerank-trace: fall-through to bare search (no rewrite, no embed)")
		return false, kgtools.ToolResult{}
	}
	args = embedded

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("server unreachable; start it with `knowledge-server`")
	}
	// Route the tail through the compile-or-DENY dispatcher: a reducible search
	// — the Router surfaces ErrNoBackend on a missing backend, which
	// renderEngineError translates into an actionable install-or-login message,
	// so the pre-flight Healthy() probe (always-local pre-FUL-323) is unnecessary.
	// compiles to Engine.Execute; an unrecognized shape is denied legibly (there is
	// no wire fallback). The per-graph search intercepts (code /
	// cloud / cicd / practice) claim the specialized shapes upstream, so only the
	// reducible knowledge/default search reaches here. The embed / rerank pre-steps
	// above are preserved verbatim; applyClientRerank below still hydrates the json
	// envelope (the engine search render in json mode emits the same
	// SearchJSONResponse shape via renderJSON).
	resp, err := engine.Dispatch(ctx, gc.Execute, "search", args)
	if err != nil {
		return true, errorResult("search call failed: " + err.Error())
	}
	if !hasRewrite || !hasReranker {
		slog.Debug("rerank-trace: skipping rerank stage",
			"has_rewrite", hasRewrite, "has_reranker", hasReranker)
		return true, resp
	}
	slog.Debug("rerank-trace: invoking applyClientRerank",
		"pool_size", widePoolSize, "top_k", widePoolTopK)
	reranker := rerank.NewVoyage(voyageKey, widePoolSize, widePoolTopK)
	return true, applyClientRerank(ctx, resp, saved, reranker)
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
	return true, composeCodeSearch(ctx, deps, exec, a, queries)
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
