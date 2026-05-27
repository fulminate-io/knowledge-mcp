// SPDX-License-Identifier: Apache-2.0

// intercept_manage_prune.go — client-side manage(prune) intercept. Prune
// hard-deletes (garbage-collects) tombstoned nodes from a graph; it drives the
// generic GraphClient.Index RPC (op INDEX_OP_PRUNE) over the resolved target
// and renders the pruned count. The server does the store work (enumerate
// tombstones, hard-delete sweep, rebuild + persist) — this handler only lowers
// the args to one IndexRequest and renders the ack.
//
// Prune is GENERIC across every graph type the server resolves; the only
// validation here is a non-empty graph (no closed allowlist, no implicit
// knowledge default) so the operator names the graph they intend to GC.

package tools

import (
	"context"
	"fmt"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleClientPrune drives the Index prune op. It requires a non-empty graph,
// parses the optional `before` cutoff (RFC3339 absolute, else relative window),
// fires ONE Index RPC, and renders the pruned count.
func handleClientPrune(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(prune): " + err.Error())
	}
	if a.Graph == "" {
		return errorResult(`manage(prune) requires "graph" — name the graph whose tombstoned nodes to garbage-collect`)
	}
	beforeNanos, perr := parsePruneBefore(a.Before)
	if perr != nil {
		return errorResult("manage(prune): " + perr.Error())
	}

	resp, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:      manageGraphSelector(a.Graph, a.Name),
		Operation:   knowledgev1.IndexRequest_INDEX_OP_PRUNE,
		BeforeNanos: beforeNanos,
	})
	if ierr != nil {
		return errorResult("manage(prune): " + ierr.Error())
	}
	return textResult(renderPruneAck(a, resp.GetAffectedCount(), beforeNanos))
}

// parsePruneBefore converts the `before` arg to an absolute unix-nanos cutoff.
// An empty string returns 0 (prune ALL tombstoned nodes). It tries an absolute
// RFC3339 timestamp first, then falls back to the relative-window grammar
// (e.g. "24h"/"2d") shared with the delete tool, subtracting the window from
// now.
func parsePruneBefore(before string) (int64, error) {
	if before == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, before); err == nil {
		return t.UnixNano(), nil
	}
	dur, err := engine.ParsePruneDuration(before)
	if err != nil {
		return 0, fmt.Errorf("unparseable before %q — use RFC3339 (2026-01-02T15:04:05Z) or a relative window (24h, 2d)", before)
	}
	return time.Now().Add(-dur).UnixNano(), nil
}

// renderPruneAck reports the pruned count + the graph target and cutoff.
func renderPruneAck(a manageArgs, pruned, beforeNanos int64) string {
	target := a.Graph
	if a.Name != "" {
		target = fmt.Sprintf("%s/%s", a.Graph, a.Name)
	}
	if beforeNanos == 0 {
		return fmt.Sprintf("Pruned %d tombstoned node(s) from %s (all tombstones).", pruned, target)
	}
	cutoff := time.Unix(0, beforeNanos).UTC().Format(time.RFC3339)
	return fmt.Sprintf("Pruned %d tombstoned node(s) from %s (tombstoned before %s).", pruned, target, cutoff)
}
