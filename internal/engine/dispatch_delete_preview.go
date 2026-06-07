// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dispatchDeletePreview is the special-shape pre-Compile seam for a dry-run
// delete (mirroring dispatchQueryByID / dispatchGraphWideEdges). A
// delete(dry_run:true) must NEVER compile to a MUTATION_KIND_DELETE — that was
// the data-loss footgun: the by-ids compile path ignored
// dry_run and really deleted. This seam claims the dry-run BEFORE Compile and
// instead issues a READ (RETURN_MODE_NODES) against the SAME selection the real
// delete would target, then renders a "Would delete N node(s)" preview. It
// deletes nothing.
//
// Two shapes, both previewable:
//   - by-ids ({ids:[...]}) → QueryPlan{Ids:[...]} (the store.ByIDs bulk read).
//   - prune-by-age ({older_than, type, session_id}) → QueryPlan{Selection:
//     pruneSelection(a)} — the EXACT selection compileDelete builds for the real
//     delete, so the preview count/set matches what a non-dry-run would remove.
//
// Returns handled=false when this is NOT a dry-run delete (so Dispatch proceeds
// to the generic Compile/exec/Render flow that performs the real delete).
func dispatchDeletePreview(ctx context.Context, exec ExecuteFn, args json.RawMessage) (kgtools.ToolResult, bool) {
	var a deleteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, false // malformed → let the generic flow surface it.
	}
	if !a.DryRun {
		return kgtools.ToolResult{}, false // a real delete is not our shape.
	}

	plan, ok := deletePreviewPlan(a)
	if !ok {
		// Same fall-through reasons the real-delete compile uses (no ids AND an
		// unknown prune type / unparseable duration). Surface a legible error
		// rather than silently doing nothing — but still never delete.
		return errorResult(
			"delete: dry_run requires either ids[] or a valid older_than + a retention-eligible type",
		), true
	}

	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: buildTarget(a.Graph, "", "", "", a.Language, ""),
	})
	if err != nil {
		return renderEngineError(err), true
	}
	nodes, derr := DecodeNodes(resp)
	if derr != nil {
		return errorResult("delete dry-run preview decode: " + derr.Error()), true
	}

	var format struct {
		Format string `json:"format"`
	}
	_ = json.Unmarshal(args, &format)
	return renderDeletePreview(nodes, format.Format), true
}

// deletePreviewPlan builds the read QueryPlan whose result set is exactly what
// the real delete would remove: the by-ids bulk read for {ids:[...]}, else the
// prune-by-age Selection read. Returns ok=false for an unrecognized shape.
func deletePreviewPlan(a deleteArgs) (*knowledgev1.QueryPlan, bool) {
	if len(a.IDs) > 0 {
		// QueryPlan.Ids lowers to store.ByIDs (RETURN_MODE_NODES) — the same bulk
		// read query(ids:[...]) uses. Reads the nodes; deletes nothing.
		return &knowledgev1.QueryPlan{Ids: a.IDs}, true
	}
	sel, ok := pruneSelection(a)
	if !ok {
		return nil, false
	}
	return &knowledgev1.QueryPlan{Selection: sel}, true
}

// renderDeletePreview renders the read-only "would delete" preview. The verb is
// deliberately "Would delete" (not "Deleted") so the dry-run output cannot be
// mistaken for a completed deletion (the old footgun rendered "Deleted N" on a
// dry-run, reinforcing the lie).
func renderDeletePreview(nodes []*knowledgev1.Node, format string) kgtools.ToolResult {
	if format == "json" {
		rows := make([]map[string]any, len(nodes))
		for i, n := range nodes {
			rows[i] = map[string]any{"id": n.GetId(), "name": n.GetSymbolName(), "type": n.GetType()}
		}
		return jsonResult(map[string]any{
			"dry_run":      true,
			"would_delete": len(nodes),
			"nodes":        rows,
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "DRY RUN — would delete %d node(s) (nothing was deleted):\n", len(nodes))
	for _, n := range nodes {
		name := n.GetSymbolName()
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&sb, "  - %s [%s] %s\n", name, n.GetType(), n.GetId())
	}
	if len(nodes) == 0 {
		sb.WriteString("  (no matching nodes — nothing would be deleted)\n")
	}
	sb.WriteString("\nRe-run without dry_run to delete.")
	return kgtools.TextResult(sb.String())
}
