// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
)

// applyClientRerank is the post-call rerank stage of InterceptSearch. It
// hydrates the server's JSON response, invokes the moved rerank package's
// reranker locally, trims to the original limit, and re-renders for the
// caller. Silent-degrades on parse/rerank errors per the "search never
// fails" contract — returns the original response unchanged rather than
// surfacing a fault that would mask real results. The error result is
// always nil today (the silent-degrade contract) but is kept on the
// signature so a future stricter mode can surface fatal client-side
// errors without a breaking call-site change.
//
// The search-result renderers + JSON hydrator were relocated to the neutral
// engine package (finding 3bdc9695) so the Phase-4 dispatcher can render
// without an engine→tools cycle; this function imports them from there.
func applyClientRerank(ctx context.Context, resp kgtools.ToolResult, saved savedState, reranker rerank.Reranker) kgtools.ToolResult {
	if !saved.clientSideActive || reranker == nil || resp.IsError {
		slog.Debug("rerank-trace: applyClientRerank guard early-return",
			"client_side_active", saved.clientSideActive,
			"reranker_nil", reranker == nil, "resp_is_error", resp.IsError)
		return resp
	}
	body := engine.FirstTextContent(resp)
	if body == "" {
		slog.Debug("rerank-trace: applyClientRerank empty body")
		return resp
	}
	hydrated, err := engine.HydrateFromJSON(body)
	if err != nil || len(hydrated) == 0 {
		// Silent-degrade — pass the server's response through untouched
		// rather than swap a real result set for an error message.
		bodyHead := body
		if len(bodyHead) > 200 {
			bodyHead = bodyHead[:200]
		}
		slog.Debug("rerank-trace: applyClientRerank hydrate failed/empty",
			"err", err, "hydrated_count", len(hydrated),
			"body_len", len(body), "body_head", bodyHead)
		return resp
	}
	reranked, err := reranker.Rerank(ctx, saved.originalQuery, hydrated)
	if err != nil {
		slog.Debug("rerank-trace: applyClientRerank reranker.Rerank error",
			"err", err.Error(), "input_count", len(hydrated))
		return resp
	}
	slog.Debug("rerank-trace: applyClientRerank success",
		"input_count", len(hydrated), "reranked_count", len(reranked),
		"original_limit", saved.originalLimit)
	if saved.originalLimit > 0 && len(reranked) > saved.originalLimit {
		reranked = reranked[:saved.originalLimit]
	}
	// Reaching this render means an embedding was attached AND rerank ran, so
	// the footer label is unconditionally "vector+rerank". The four degrade
	// branches above early-return the untouched server response (no footer) —
	// a deliberately-deferred visibility gap, not retrofitted here.
	return engine.RenderForCaller(saved.originalQuery, reranked, saved.originalFormat, saved.originalFields, "vector+rerank")
}
