// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_cache.go — client-side manage(rebuild_cache)
// intercept. rebuild_cache DROPS a builtin graph's per-graph content-hash
// caches (summary + embed) — the code graph (keyed per repo) or the knowledge
// graph (keyed on its "default" instance) — and RE-DERIVES them from the CURRENT
// base-graph nodes with ZERO model calls. It is the ONLY escape hatch for the
// caches — a FREE re-derivation, NOT a "clear" (a clear would guarantee a full
// re-pay). It serves recovery (lost/corrupted cache), manual invalidation (the
// deferred model/prompt-change lever), and backfill/migration (graphs populated
// before the feature shipped). The server does the drop + re-derive work; this
// handler only lowers the args to one IndexRequest and renders the started-ack.

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleClientRebuildCache drives the Index rebuild_cache op. The content-hash
// caches exist for the builtin code and knowledge graphs (v1), so it requires
// graph=code (name=repo) or graph=knowledge (name defaults to "default", the one
// canonical instance — BASE layer only, no "@"-overlay names). Overlay names are
// rejected symmetrically with rebuild_segments. Fires ONE Index RPC and renders
// the started-ack.
func handleClientRebuildCache(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(rebuild_cache): " + err.Error())
	}
	if a.Graph != string(kgtypes.GraphCode) && a.Graph != string(kgtypes.GraphKnowledge) {
		return errorResult(`manage(rebuild_cache) requires graph="code" or graph="knowledge" — the content-hash caches are builtin-graph only`)
	}
	// The builtin knowledge graph has one canonical instance named "default"; an
	// empty name (or the "knowledge" alias) resolves to it. BASE layer only in v1 —
	// an "@"-suffixed overlay/session name is rejected, mirroring rebuild_segments
	// so the two operator levers treat overlay names symmetrically.
	if a.Graph == string(kgtypes.GraphKnowledge) {
		if strings.ContainsRune(a.Name, '@') {
			return errorResult(fmt.Sprintf(`manage(rebuild_cache): knowledge overlay name %q is not supported — overlay rebuilds not supported in v1 (base "default" layer only)`, a.Name))
		}
		if a.Name == "" || a.Name == string(kgtypes.GraphKnowledge) {
			a.Name = "default"
		}
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
		"rebuild_cache started for %s/%s — dropping + re-deriving the summary/embed caches "+
			"from base nodes in the background (no model calls). Watch the server logs for "+
			"\"rebuild_cache.complete\" to confirm completion.",
		a.Graph, a.Name))
}
