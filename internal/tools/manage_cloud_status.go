// SPDX-License-Identifier: Apache-2.0

// manage_cloud_status.go — the CLOUD arm of manage(status).
//
// Lifted out of manage.go verbatim when that file reached the repo's 500-line
// cap. Nothing about the behavior moved: this is handleCloudStatus and its doc
// comment, in the same package, called from the same one place
// (handleServerStatus's logged-in branch).

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleCloudStatus reports the CLOUD graph stats for a logged-in user via
// the already-routed Stats RPC (deps.GraphCaller() is the *Router,
// which satisfies the statsRPC seam). The TEXT body renders the shared
// engine.RenderStatsBreakdown under a "Backend: cloud (<host>)" preamble and
// omits the local-daemon-only fields (GraphStats carries none of them). The JSON
// body, by contrast, LAYERS the local-daemon fields (pid, graph_path, pipeline
// counters, coverage[], doctor[]) via addLocalDaemonJSON and then overwrites the
// node/edge/vector totals with the CLOUD figures on top — so the Daemon Status
// web page shows every card even when logged in (CEO: always show local-daemon
// fields). The empty GraphSelector targets the default knowledge graph, identical
// to intercept_query_stats.go.
func handleCloudStatus(ctx context.Context, deps ClientDeps, host, format string) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(status): graph client unavailable")
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return errorResult("manage(status): stats seam unavailable")
	}
	resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target: &knowledgev1.GraphSelector{Graph: ""},
	})
	if err != nil {
		return errorResult("manage(status): cloud stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	clientVer, daemonVer, daemonKnown := versionSection(deps)
	serverBinVer, serverBinKnown := serverBinarySection(deps)
	if format == "json" {
		// Local-daemon facts first (pid/graph_path/pipeline/coverage[]/doctor[]/
		// transcript/collect_runs + any local node/edge counts) …
		m := map[string]any{}
		addLocalDaemonJSON(ctx, deps, m)
		// … then the cloud identity + CLOUD graph totals layered ON TOP,
		// overwriting any local node/edge/vector counts with the cloud figures.
		m["status"] = "running"
		m["backend"] = "cloud"
		m["host"] = host
		m["nodes"] = stats.GetNodeCount()
		m["edges"] = stats.GetEdgeCount()
		m["binary_vectors"] = stats.GetBinaryVectorCount()
		addVersionJSON(m, clientVer, daemonVer, daemonKnown, serverBinVer, serverBinKnown)
		addClientVersionStateJSON(m)
		return jsonResult(m)
	}
	transcriptBlock := ""
	if th, ok := transcriptUploadHealth(deps); ok {
		transcriptBlock = renderTranscriptHealthText(th)
	}
	if uh, ok := updateCheckHealth(deps); ok {
		transcriptBlock += renderUpdateHealthText(uh)
	}
	return textResult(fmt.Sprintf(
		"## Graph server: cloud\n  Backend: cloud (%s)%s%s\n\n%s%s%s%s%s",
		host, renderHarnessSessionText(ctx), renderAccountStatusText(ctx, deps),
		engine.RenderStatsBreakdown(stats), renderLLMCoverage(ctx, deps), transcriptBlock,
		collectRunSection(deps),
		renderVersionLines(clientVer, daemonVer, daemonKnown, serverBinVer, serverBinKnown)+renderClientVersionStateLines()))
}
