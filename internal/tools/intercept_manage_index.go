// SPDX-License-Identifier: Apache-2.0

// intercept_manage_index.go — client-side manage intercepts that drive the
// GraphClient.Index RPC: set_metadata_overrides / delete_branch / list_branches.
// Each is a thin client handler that lowers the manage args to one IndexRequest,
// fires Index, and renders the ack/overlay-list client-side. The store logic is
// delegated server-side via the generic Index op; these handlers re-implement only
// the LLM-facing rendering. (rebuild_bm25 / rebuild_hnsw were retired with the
// server search subsystem.)

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Indexer is the narrow Index-RPC seam the manage intercepts drive. The
// production graphClientCaller implements it (Call + Execute + Index); tests
// inject a recording fake. Mirrors render.Executor — type-asserted from the
// GraphCaller so the Call-only tools.GraphCaller interface stays unwidened.
type Indexer interface {
	Index(ctx context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error)
}

// manageIndexer upgrades deps.GraphCaller() to the Indexer seam, or returns a
// typed error so the missing seam is loud.
func manageIndexer(deps ClientDeps) (Indexer, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("GraphCaller is unavailable — the client is running in degraded mode")
	}
	ix, ok := gc.(Indexer)
	if !ok {
		return nil, fmt.Errorf("manage requires an Index-capable graph client")
	}
	return ix, nil
}

// manageGraphSelector builds the Index RPC GraphSelector for a manage op,
// routing the operator-supplied name to the field the target graph REQUIRES. An
// empty graph is the knowledge default. Each graph type validates its own
// required selector field server-side (e.g. graph=code requires repo), so a
// name lowered onto the wrong field is rejected — mirrors the render
// graphTarget routing and the server resolveTargetDB expectations.
//
// WHICH FIELD THAT IS COMES FROM graphsel AND NOWHERE ELSE. This function used
// to restate the whole partition in a four-arm switch (code→Repo,
// practice→Language, everything else→Name), which is one of
// the copies engine.mutateTarget's doc records as having produced two production
// defects. Practice becoming a singleton is exactly the update that copy would
// have missed, and it would have missed it across all six of this builder's
// callers at once — set_metadata_overrides, rebuild_cache, prune, drop_graph,
// sync push and repair_edges.
//
// THE KNOWLEDGE SPECIAL CASE STAYS HAND-WRITTEN, and that is a decision rather
// than a leftover. It suppresses the names {"", "default", "knowledge"} for a
// family graphsel classifies as FieldName, and graphsel's own omitDefaultName
// does not express it: that flag suppresses "" and "default" and not
// "knowledge". Folding it into the derivation would start sending a name the
// server's knowledge alias arm happens to tolerate, which is a behavior change
// dressed as a simplification.
func manageGraphSelector(graph, name string) *knowledgev1.GraphSelector {
	if graph == "" || graph == string(kgtypes.GraphKnowledge) {
		// The knowledge graph always uses the default instance; an explicit
		// "default"/"knowledge" name is the same target. Leave the selector
		// graph set so the server's resolveTargetDB lands on knowledge.
		sel := &knowledgev1.GraphSelector{Graph: graph}
		if name != "" && name != "default" && name != "knowledge" {
			sel.Name = name
		}
		return sel
	}
	// THE INSTANCE-KEY PARTITION IS graphsel's, not a switch of this function's
	// own. A hand-maintained copy is what a corpus check flags here, and the
	// reason is measured rather than stylistic: this function carried a cloud
	// arm until the family was removed, and a copy that had gone stale instead
	// would have put the name on a field the resolver does not read.
	return graphsel.GraphSelectorFor(kgtypes.GraphType(graph), name, false)
}

// handleClientSetMetadataOverrides drives the Index set_metadata_overrides op:
// it lowers the force_scalar / force_edge lists onto the Index RPC params
// (comma-joined, the wire shape overrideConfigFromParams reads), fires ONE Index
// RPC (the server self-persists via SaveOverrideConfig — no extra persist op),
// and renders the ack via a port of the retired server-side ack formatter.
func handleClientSetMetadataOverrides(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(set_metadata_overrides): " + err.Error())
	}
	scalar := normalizeOverrideKeys(a.ForceScalar)
	edge := normalizeOverrideKeys(a.ForceEdge)
	if len(scalar) == 0 && len(edge) == 0 {
		return errorResult("manage(set_metadata_overrides): at least one of force_scalar or force_edge must be non-empty")
	}
	params := map[string]string{}
	if len(scalar) > 0 {
		params["force_scalar"] = strings.Join(scalar, ",")
	}
	if len(edge) > 0 {
		params["force_edge"] = strings.Join(edge, ",")
	}
	if _, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    manageGraphSelector(a.Graph, a.Name),
		Operation: knowledgev1.IndexRequest_INDEX_OP_SET_METADATA_OVERRIDES,
		Params:    params,
	}); ierr != nil {
		return errorResult("manage(set_metadata_overrides): " + ierr.Error())
	}
	gt, name := overrideTargetLabels(a)
	return textResult(renderOverrideAck(gt, name, scalar, edge))
}

// normalizeOverrideKeys trims whitespace and drops empty entries (mirrors the
// trimming the retired server-side override config builder did).
func normalizeOverrideKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if t := strings.TrimSpace(k); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// overrideTargetLabels returns the (graph_type, name) labels for the ack. The
// knowledge graph always labels its instance "default" (parity with the
// allow-empty-name knowledge case of the retired server-side handler).
func overrideTargetLabels(a manageArgs) (string, string) {
	gt := a.Graph
	if gt == "" {
		gt = string(kgtypes.GraphKnowledge)
	}
	name := a.Name
	if gt == string(kgtypes.GraphKnowledge) && name == "" {
		name = "default"
	}
	return gt, name
}

// renderOverrideAck ports the retired server-side ack formatter — lists every
// key in both buckets + the replace-not-merge reminder.
func renderOverrideAck(gt, name string, scalar, edge []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "metadata override config saved for %s/%s\n", gt, name)

	sb.WriteString("force_scalar: ")
	if len(scalar) == 0 {
		sb.WriteString("(empty)\n")
	} else {
		fmt.Fprintf(&sb, "[%s]\n", strings.Join(scalar, ", "))
	}

	sb.WriteString("force_edge:   ")
	if len(edge) == 0 {
		sb.WriteString("(empty)\n")
	} else {
		fmt.Fprintf(&sb, "[%s]\n", strings.Join(edge, ", "))
	}

	sb.WriteString("\nThis replaces the previous override config — keys absent from the new lists ")
	sb.WriteString("fall back to the dream PROMOTE phase's auto-tuned representation. ")
	sb.WriteString("NodeProxy metadata is always pinned to the scalar map regardless of override.")
	return sb.String()
}

// handleClientDeleteBranch drives the Index delete_branch op over the injected
// repo (a.Name) + the branch overlay name (a.Branch). The server pre-flights
// existence and returns NotFound on a miss; a missing branch surfaces that error
// verbatim. Renders the deletion ack the retired server-side handler produced.
func handleClientDeleteBranch(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(delete_branch): " + err.Error())
	}
	if a.Name == "" {
		return errorResult("manage(delete_branch): repo is required; run from inside an indexed code repo or pass name:")
	}
	if a.Branch == "" {
		return errorResult("manage(delete_branch): branch is required")
	}
	if _, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    branchGraphSelector(a),
		Operation: knowledgev1.IndexRequest_INDEX_OP_DELETE_BRANCH,
		Params:    map[string]string{"repo": a.Name, "branch": a.Branch},
	}); ierr != nil {
		return errorResult("manage(delete_branch): " + ierr.Error())
	}
	return textResult(fmt.Sprintf("Branch graph %q/%q deleted.", a.Name, a.Branch))
}

// handleClientListBranches drives the Index list_branches op and renders the
// overlay list — a markdown table or a JSON payload per a.Format, both ported
// from the retired server-side list-branches handler.
func handleClientListBranches(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(list_branches): " + err.Error())
	}
	if a.Name == "" {
		return errorResult("manage(list_branches): repo is required; run from inside an indexed code repo or pass name:")
	}
	resp, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    branchGraphSelector(a),
		Operation: knowledgev1.IndexRequest_INDEX_OP_LIST_BRANCHES,
	})
	if ierr != nil {
		return errorResult("manage(list_branches): " + ierr.Error())
	}
	overlays := resp.GetBranches()
	if a.Format == "json" {
		return jsonResult(map[string]any{"repo": a.Name, "total": len(overlays), "branches": overlays})
	}
	return textResult(renderBranchTable(a.Name, overlays))
}

// branchGraphSelector targets the code graph for branch ops (the only graph type
// that carries branch overlays via the manage surface), keyed by the injected
// repo name.
// The code family selects by repo and carries no name, so the server's per-family selector partition does not reject the call.
func branchGraphSelector(a manageArgs) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: a.Name}
}

// renderBranchTable ports the retired server-side markdown table. Ranges
// the proto branch carriers by pointer (the proto GraphInfo holds a noCopy
// MessageState — a value range would copylocks).
func renderBranchTable(repo string, branches []*knowledgev1.GraphInfo) string {
	if len(branches) == 0 {
		return fmt.Sprintf("No branch graphs found for repo %q.", repo)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Branch overlays for %q (%d available)\n\n", repo, len(branches))
	sb.WriteString("| Branch | Status | Size | Nodes | Edges |\n")
	sb.WriteString("|--------|--------|------|-------|-------|\n")
	for _, b := range branches {
		status := "on disk"
		nodesStr, edgesStr := "-", "-"
		if b.GetLoaded() {
			status = "**loaded**"
			nodesStr = fmt.Sprintf("%d", b.GetNodes())
			edgesStr = fmt.Sprintf("%d", b.GetEdges())
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n", b.GetName(), status, formatManageBytes(b.GetFileSize()), nodesStr, edgesStr)
	}
	return sb.String()
}

// formatManageBytes ports the retired server-side byte formatter.
func formatManageBytes(b int64) string {
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
