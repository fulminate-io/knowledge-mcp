// SPDX-License-Identifier: Apache-2.0

// Package rerank holds the LLM-driven candidate re-scoring + DSL
// pipeline that used to live in domains/store. Operates on
// engine.SearchResult (Node + Score) — the client-side search-hit DTO
// carrying the wire proto *knowledgev1.Node directly (the legacy node wrapper
// layer was dropped from the client read path).
//
// Dep direction: rerank → engine (one-way, for the SearchResult DTO) and
// rerank → kgtypes (node-behavior free funcs). engine does NOT import
// rerank; the prod wiring threads engine.HydrateFromJSON's []SearchResult
// straight into Reranker.Rerank (cmd/knowledge/internal/tools/search_rerank.go).
package rerank

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// Reranker is the public interface for cross-encoder rerankers.
// Concrete implementations in this package (e.g. *voyageReranker)
// re-score a candidate set and return it re-ordered.
//
// On error: return the input slice unmodified plus the error. The
// caller (applyClientRerank) logs and falls back to the unreranked
// fan-in result.
type Reranker interface {
	Rerank(ctx context.Context, query string, results []engine.SearchResult) ([]engine.SearchResult, error)
}
