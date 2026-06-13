// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_adjacency.go — client-side claim for
// thoughts(operation:adjacency). The legacy server-side dispatch case was
// deleted during the engine-only cutover without being re-claimed in the
// client intercept chain; this restores it as a thin parse → FetchAdjacency →
// render handler. All adjacency logic (scope validation, the single bulk edges read,
// session-sibling expansion, subset projection) lives in fetchAdjacency
// (cmd/knowledge/internal/thought/wire_adjacency.go) — nothing is reimplemented
// here.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// adjacencyClientArgs is the parsed thoughts(operation:adjacency) shape:
// scope selects the node set ("all" thought-only with session-sibling
// expansion, or "all_types" every non-proxy node), thought_ids optionally
// projects the result down to that subset, and format selects the render arm.
type adjacencyClientArgs struct {
	Scope      string   `json:"scope"`
	ThoughtIDs []string `json:"thought_ids"`
	Format     string   `json:"format"`
}

// handleAdjacencyClient claims thoughts(operation:adjacency). It parses the
// args, nil-checks the graph client (a plain GraphCaller engine read — no
// readiness/forcer/segment gate, those serve immediately after bind), and
// delegates entirely to FetchAdjacency, which validates the scope and returns
// the loud empty/unknown-scope error this handler surfaces.
//
// UNBOUNDED RESULT: scope:"all_types" is intentionally an UNBOUNDED full-graph
// bulk read — it reads the WHOLE non-proxy node set (~66K nodes today) with no
// cap and no budget, unlike the daemon's internal cluster-detection consumer
// which only ever uses scope:"all" under a 5-minute pass budget. This is the
// documented contract: the schema advertises all_types as the cross-type bulk
// read for PROGRAMMATIC consumers issuing a deliberate bulk read, so there is
// deliberately no cap. The json arm returns the full {node_ids, adjacency} map;
// the text arm returns COUNTS ONLY (never the map) precisely so an interactive
// caller is not handed a multi-megabyte dump.
func handleAdjacencyClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a adjacencyClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("adjacency: graph client unavailable")
	}

	nodeIDs, adj, err := clientthought.FetchAdjacency(ctx, gc, a.Scope, a.ThoughtIDs)
	if err != nil {
		return errorResult(err.Error())
	}

	if a.Format == "json" {
		return jsonResult(map[string]any{"node_ids": nodeIDs, "adjacency": adj})
	}
	return textResult(formatAdjacencySummary(nodeIDs, adj))
}

// formatAdjacencySummary renders the text arm by COUNT only — it never marshals
// the adjacency map itself (see the unbounded-result note on
// handleAdjacencyClient). It reports the total node_ids count, the total
// neighbor count, and a per-node "id: N neighbors" line, sorted for stable
// output.
func formatAdjacencySummary(nodeIDs []string, adj map[string][]string) string {
	totalNeighbors := 0
	for _, sibs := range adj {
		totalNeighbors += len(sibs)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Adjacency: %d node_ids, %d total neighbor edges.\n", len(nodeIDs), totalNeighbors)
	ids := make([]string, len(nodeIDs))
	copy(ids, nodeIDs)
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(&sb, "  %s: %d neighbors\n", id, len(adj[id]))
	}
	return sb.String()
}
