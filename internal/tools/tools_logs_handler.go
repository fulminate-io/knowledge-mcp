// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleTraverse is the unified-traverse client-side dispatcher used by the
// moved log traversal tests. Production code reaches the moved
// traverseLogs method via the InterceptLogsTraversal chain step; this
// helper exists so test fixtures that JSON-encode args land on the same
// dispatch path the production InterceptLogsTraversal would take.
//
// Kept narrow on purpose — only graph='logs' is supported here,
// because every other graph routes through the server unchanged. Reaching
// this with non-logs args returns kgtools.ErrorResult so the caller
// surfaces a clear "wrong dispatcher" error.
func (h *Handler) handleTraverse(ctx context.Context, raw []byte) kgtools.ToolResult {
	var a traverseArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return kgtools.ErrorResult("invalid arguments: " + decodeArgsError(raw, err))
	}
	if a.Graph != "logs" {
		return kgtools.ErrorResult("client-side handleTraverse only handles graph='logs'; got " + a.Graph)
	}
	if a.Direction == "" {
		a.Direction = "out"
	}
	return h.traverseLogs(ctx, a)
}

// Handler is the client-side companion to cmd/knowledge-server/tools.Handler,
// narrowed to the surface the moved log-tool dispatchers need.
//
// Handler now owns getOrFetchLogState, the wire-fetch orchestrator
// that bulk-loads templates/streams/chunks/labels/proxies + edges from the
// server in four RPCs and assembles a *logState the formatters consume.
// No local store.Store() reads anywhere — handlers operate purely on
// pre-fetched data.
type Handler struct {
	// Deps is the standard client dependency surface. Optional: a nil Deps
	// is tolerated for tests that exercise pure-format helpers without
	// touching the wire-fetch path. Tests that drive the full handler
	// chain inject a fake GraphCaller via setupLogTestHandler.
	Deps ClientDeps

	// graphCallerOverride lets tests substitute a fake GraphCaller without
	// constructing the full ClientDeps stack. Production code never sets
	// this — production callers always go through Deps.GraphCaller().
	graphCallerOverride GraphCaller
}

// testFallbackGraphCaller is a test-only seam: when tests set this hook
// (typically via setupLogTestHandler in Phase 4 OR the legacy
// store-backed caller helper today), Handler.graphCaller() returns its
// result when no override / Deps are configured. Production code never
// sets this — the variable is nil at startup, so production callers
// always go through Deps.GraphCaller() (or fail fast when Deps is nil).
var testFallbackGraphCaller func() GraphCaller //nolint:gochecknoglobals // test-seam, see comment

// graphCaller returns the GraphCaller to use for wire-fetch RPCs. Tests
// can set graphCallerOverride to inject a fake without standing up the
// full ClientDeps stack.
func (h *Handler) graphCaller() GraphCaller {
	if h == nil {
		return nil
	}
	if h.graphCallerOverride != nil {
		return h.graphCallerOverride
	}
	if h.Deps != nil {
		return h.Deps.GraphCaller()
	}
	if testFallbackGraphCaller != nil {
		return testFallbackGraphCaller()
	}
	return nil
}

// formatBytes mirrors the server-side helper in tools_branch.go. Duplicated
// because cmd/knowledge-server cannot import cmd/knowledge/internal.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// handleLogsStats renders the stats body for a logs queryID graph via the
// Stats RPC + the Phase-1 shared RenderStatsBreakdown. The earlier
// stub deferred this on the missing shared stats helpers (fetchStats /
// formatStatsBreakdown / appendSampleNames) — those now exist client-side, so
// the logs stats path renders the same uniform body every other graph type
// emits. The logs graph is the real persisted graph (Graph:"logs", Name:queryID);
// the pre-fetched *logState is no longer needed for the counts. format carries
// the query's format arg threaded from handleLogsQuery (a.Format) so the
// format=="json" branch can return structured GraphStats.
func (h *Handler) handleLogsStats(ctx context.Context, queryID string, _ *logState, _ bool, format string) kgtools.ToolResult {
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("logs stats: graph client unavailable")
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return kgtools.ErrorResult("logs stats: stats seam unavailable")
	}
	resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target: &knowledgev1.GraphSelector{Graph: "logs", Name: queryID},
	})
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("logs %q graph stats failed: %s", queryID, err.Error()))
	}
	stats := resp.GetGraphStats()
	if format == "json" {
		return jsonResult(map[string]any{
			"graph":               "logs",
			"name":                queryID,
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		})
	}
	return kgtools.TextResult(fmt.Sprintf("## Logs Graph: %s\n\n%s", queryID, engine.RenderStatsBreakdown(stats)))
}

// logStateEdgeTypes is the union of every edge type the moved log
// handlers iterate. fetchAllLogEdges narrows the graph-wide enumeration
// to this set so we don't drag every edge in the graph back to the
// client.
var logStateEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeContains,
	kgtypes.EdgeCorrelatesWith,
	kgtypes.EdgeHasLabel,
	kgtypes.EdgeEmittedBy,
	kgtypes.EdgeBelongsTo,
}

// getOrFetchLogState is the wire-fetch orchestrator. Four bulk
// RPCs against the GraphCaller:
//   - fetchAllLogNodes → templates + streams + chunks (3 RPCs)
//   - fetchAllLogAuxNodes → labels + proxies (2 RPCs)
//   - fetchAllLogEdges → all edges of logStateEdgeTypes (1 RPC, graph-wide)
//
// Returns (engine, st, err). engine is built from the fetched
// templates/streams/chunks via logs.NewQueryEngine — the unchanged
// constructor; the engine doesn't know it came from the wire. st is the
// pre-fetched view consumed by every formatter.
//
// No cache — every MCP call refetches. Server-side
// dirty_gen tracking would be premature optimization; refetching at
// ~tens-of-ms cost is acceptable until profiling says otherwise.
func (h *Handler) getOrFetchLogState(ctx context.Context, queryID string) (*logs.QueryEngine, *logState, error) {
	gc := h.graphCaller()
	if gc == nil {
		return nil, nil, fmt.Errorf(
			"client-side log handlers require a GraphCaller (no Deps.GraphCaller() configured for queryID=%q)",
			queryID)
	}
	templates, streams, chunks, err := fetchAllLogNodes(ctx, gc, queryID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch log nodes for %q: %w", queryID, err)
	}
	labels, proxies, err := fetchAllLogAuxNodes(ctx, gc, queryID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch log aux nodes for %q: %w", queryID, err)
	}
	edges, err := fetchAllLogEdges(ctx, gc, queryID, logStateEdgeTypes)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch log edges for %q: %w", queryID, err)
	}

	st := newLogState(templates, streams, chunks, labels, proxies, edges)

	if len(templates) == 0 && len(streams) == 0 && len(chunks) == 0 {
		return nil, st, nil
	}
	// Engine rebuild uses the unchanged logs.NewQueryEngine constructor —
	// it takes raw logwire struct slices, not a DB handle. The
	// templates/streams/chunks → logwire conversion helpers reuse
	// templateFromNode / streamFromNode / chunkFromNode from
	// tools_logs_query_rebuild.go.
	engine := logs.NewQueryEngine(
		streamsAsWire(streams),
		chunksAsWire(chunks),
		templatesAsWire(templates),
	)
	return engine, st, nil
}
