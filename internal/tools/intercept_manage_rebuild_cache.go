// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_cache.go — client-side manage(rebuild_cache)
// intercept. rebuild_cache DROPS a code repo's per-repo content-hash
// caches (summary + embed) and RE-DERIVES them from the CURRENT base-graph nodes
// with ZERO model calls. It is the ONLY escape hatch for the caches — a FREE
// re-derivation, NOT a "clear" (a clear would guarantee a full re-pay). It serves
// recovery (lost/corrupted cache), manual invalidation (the deferred
// model/prompt-change lever), and backfill/migration (repos collected before the
// feature shipped). The server does the drop + re-derive work; this handler only
// lowers the args to one IndexRequest and renders the count.

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleClientRebuildCache drives the Index rebuild_cache op. The caches are
// code-only (v1), so it requires graph=code with a non-empty name (repo). Fires
// ONE Index RPC and renders the re-derived entry count.
func handleClientRebuildCache(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(rebuild_cache): " + err.Error())
	}
	if a.Graph != "code" {
		return errorResult(`manage(rebuild_cache) requires graph="code" — the content-hash caches are code-only`)
	}
	if a.Name == "" {
		return errorResult(`manage(rebuild_cache) requires "name" — the repo whose caches to re-derive`)
	}

	if _, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    manageGraphSelector(a.Graph, a.Name),
		Operation: knowledgev1.IndexRequest_INDEX_OP_REBUILD_CACHE,
	}); ierr != nil {
		return errorResult("manage(rebuild_cache): " + ierr.Error())
	}
	// The op is ASYNC: the server drops + re-derives the caches on a background
	// goroutine and acknowledges immediately (no derived count is known at
	// return). The operator confirms completion via the server logs
	// ("rebuild_cache.complete").
	return textResult(fmt.Sprintf(
		"rebuild_cache started for code/%s — dropping + re-deriving the summary/embed caches "+
			"from base nodes in the background (no model calls). Watch the server logs for "+
			"\"rebuild_cache.complete\" to confirm completion.",
		a.Name))
}
