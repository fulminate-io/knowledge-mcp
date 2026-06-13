// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// bornLinkCodeEdges extracts code referents from summary+content, resolves them
// to knowledge-graph proxy IDs (resolve-or-drop), and returns one
// thought--relates-to-->proxy BatchEdge per resolved proxy, each tagged
// Method=code-ref and originating at the thought slot (FromIdx 0) with the proxy
// as an existing-node TO (ToIdx -1). Returns nil when nothing extracts or
// resolves — the caller appends the result to the thought's create_batch edges so
// the born-links ride atomically with the thought create.
func bornLinkCodeEdges(ctx context.Context, gc GraphCaller, summary, content string) []kgwire.BatchEdge {
	proxyIDs := resolveCodeReferents(ctx, gc, extractCodeReferents(summary, content))
	if len(proxyIDs) == 0 {
		return nil
	}
	out := make([]kgwire.BatchEdge, 0, len(proxyIDs))
	for _, proxyID := range proxyIDs {
		if proxyID == "" {
			continue
		}
		out = append(out, kgwire.BatchEdge{
			FromIdx: 0,
			ToIdx:   -1,
			ToID:    proxyID,
			Type:    kgtypes.EdgeRelatesTo,
			Method:  codeRefMethod,
		})
	}
	return out
}

// codeRefMethod is the Method tag stamped on every born-link relates-to edge
// (thought→code-proxy) so the provenance of these edges is filterable: a
// relates-to edge carrying Method="code-ref" was minted by code-referent
// extraction, distinct from a user-cited links[] relates-to (no Method).
const codeRefMethod = "code-ref"

// resolveCodeReferents resolves each extracted code referent against the loaded
// code graphs and returns the deterministic cross-graph proxy IDs for those that
// resolve, DROPPING (with a debug log) any that do not. It is the code-only
// resolve-OR-DROP sibling of resolveCrossGraphID (intercept_thoughts_charge.go):
// same locate→build+upsert proxy shape, but it omits the raw-id fall-through —
// an unresolvable referent is dropped, never returned as a dangling edge target.
//
// Probe scope is code graphs ONLY: it enumerates with the thin
// ListForeignGraphsOfType(GraphCode) (one graph-names read) rather than the
// four-read ListForeignGraphs, so cloud/practice/cicd graphs are never fetched
// against. The proxy is upserted with targetGraph="knowledge" (mirroring
// resolveCrossGraphID) because the born-link relates-to edge rides the knowledge
// create_batch and the server resolves the edge's ToID inside the knowledge graph;
// a code-graph-scoped proxy would not be found and would abort the create_batch.
//
// A nil Execute seam (gc not Execute-capable) yields no proxies — the think still
// proceeds, just without born-links. Empty input returns nil with no reads.
func resolveCodeReferents(ctx context.Context, gc GraphCaller, referents []string) []string {
	if gc == nil || len(referents) == 0 {
		return nil
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		// No Execute carrier → cannot probe foreign graphs; skip born-linking.
		slog.Debug("code-referent resolve: Execute seam unavailable, skipping", "error", err)
		return nil
	}
	graphs, err := crossgraph.ListForeignGraphsOfType(ctx, ex, string(kgtypes.GraphCode))
	if err != nil {
		slog.Debug("code-referent resolve: code-graph enumeration failed, skipping", "error", err)
		return nil
	}
	if len(graphs) == 0 {
		return nil
	}
	return resolveCodeReferentsWith(ctx, gc, ex, graphs, referents)
}

// resolveCodeReferentsWith is the inner resolve-or-drop loop that takes the code
// graphs ALREADY enumerated by the caller — so a bulk caller (the backfill) lists
// the code graphs ONCE and reuses the list across every thought instead of paying
// one graph-names read per thought. resolveCodeReferents lists the graphs once then
// delegates here; the backfill calls this directly with its hoisted code-graph
// list. ex is the Execute seam the proxy upsert rides; graphs are code-only.
func resolveCodeReferentsWith(ctx context.Context, gc GraphCaller, ex render.Executor, graphs []crossgraph.ForeignGraph, referents []string) []string {
	var out []string
	for _, ref := range referents {
		gt, name, node, found := crossgraph.LocateForeignNode(ctx, gc, graphs, ref)
		if !found {
			slog.Debug("code-referent resolve: dropped unresolvable referent", "referent", ref)
			continue
		}
		proxy, uerr := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", gt, name, ref, node)
		if uerr != nil {
			slog.Debug("code-referent resolve: dropped referent on proxy upsert failure",
				"referent", ref, "error", uerr)
			continue
		}
		out = append(out, proxy.GetId())
	}
	return out
}
