// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_metadata_stats_topology.go is the client-side claim for
// query(mode:metadata_stats). The sibling topology mode is handled by the
// extended InterceptTopology (intercept_topology.go) — dead_code stays on the
// client-RTA path, other analyzers route through the Topology RPC.
//
// metadata_stats recipe (server handleMetadataStats, tools_query_metadata_stats.go):
// the MetadataStats RPC returns BOTH carriers — the typed MetadataStats AND the
// typed OverrideConfig (engine_stats.go marshals db.OverrideConfig() alongside).
// The composer reads BOTH off the response and threads the OverrideConfig into
// engine.BuildMetadataStatsRows → engine.RecommendAction. RecommendAction's two
// highest-precedence rules are the ForceEdge/ForceScalar override checks, so
// reading only the stats carrier (nil OverrideConfig) would silently mis-recommend
// any operator-pinned key — threading the OverrideConfig is load-bearing, not
// optional.

// InterceptQueryMetadataStats claims query(mode:metadata_stats).
func InterceptQueryMetadataStats(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "metadata_stats" {
		return false, kgtools.ToolResult{}
	}
	if err := accountQueryParams(armMetadataStats, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("metadata_stats: graph client unavailable")
	}
	mc, ok := gc.(metadataStatsCaller)
	if !ok {
		return true, errorResult("metadata_stats: stats seam unavailable")
	}

	resp, err := mc.MetadataStats(ctx, &knowledgev1.MetadataStatsRequest{Target: domainTarget(a)})
	if err != nil {
		return true, errorResult(fmt.Sprintf("metadata stats load failed: %v", err))
	}
	// The typed MetadataStats + OverrideConfig ride the response carriers directly
	// (nil-safe getters). The OverrideConfig is the REQUIRED second carrier —
	// engine.BuildMetadataStatsRows → engine.RecommendAction applies its
	// ForceEdge/ForceScalar precedence; a nil OverrideConfig simply means no pins.
	stats := resp.GetMetadataStats()
	override := resp.GetOverrideConfig()

	label := domainGraphLabel(a)
	rows := engine.BuildMetadataStatsRows(stats, override)
	if a.Format == "json" {
		// `account` IS NOT PASSED, and that is the ruling rather than an omission:
		// it keys no surviving family, so emitting it as a payload key made the
		// json body differ between a call with the (accepted and ignored)
		// parameter and the same call without it.
		return true, jsonResult(engine.MetadataStatsJSONPayload(label, a.Name, a.Language, rows))
	}
	if len(rows) == 0 {
		return true, textResult(fmt.Sprintf("No metadata stats yet for %s graph. %s", label, metadataStatsCollectHintClient(a)))
	}
	return true, textResult(engine.RenderMetadataStatsTable(label, rows))
}

// metadataStatsCollectHintClient ports the server metadataStatsCollectHint —
// the context-sensitive repopulation hint for the empty-stats message.
func metadataStatsCollectHintClient(a queryArgs) string {
	switch a.Graph {
	case "practice":
		return "Practice graph stats refresh when the graph is repopulated."
	case "linkage":
		// THE ONE SURVIVING LINKER PASS, read at linker/client.go:66 (RunAll) and
		// linker/dockerfile.go:23 (LinkDockerfiles). It runs from
		// manage(operation:"link"), which sweeps every code graph, and from the
		// post-collect tail of a CODE collect, which runs the same pass scoped to
		// the graph that was just collected — and from nothing else. THE TRIGGER
		// MOVED HERE FROM THE CI/CD COLLECT when that family was retired: the pass
		// reads code graphs on both sides, so it now fires where its input changes
		// rather than where an inherited allowlist put it.
		return "Linkage stats refresh when the cross-graph linker runs: `manage(operation: \"link\")`, or a code collect, which runs the same pass."
	default:
		return "Run any collect/mutate against this graph; stats refresh at PROMOTE."
	}
}
