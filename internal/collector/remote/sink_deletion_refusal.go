// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// sink_deletion_refusal.go — what the client does with
// FinalizeResponse.deletion_refused_reason.
//
// UNTIL THIS EXISTED, NOTHING READ THAT FIELD. The server has named the guard
// that stopped a deletion phase since the guard stack landed, and the client
// logged "finalize accepted" at Debug and reported success either way — so a
// refused deletion and a graph with nothing to delete were the same observation
// from the operator's seat, and rows the client had correctly named as gone stayed
// live with nothing to act on. That is the indistinguishability the field exists
// to end, and reading it is the whole of this file.

// reportDeletionRefusal reports a refused deletion phase on both channels the
// collect has: the daemon log, for whoever is watching the process, and the
// context's refusal recorder, which reaches the caller's result text.
//
// IT IS SILENT ON AN ADMITTED DELETION, which is what makes the Error level
// meaningful: a line per collect would be noise, and the whole point of the level
// is that the message is actionable.
//
// THE COUNTS ARE THE CLIENT'S OWN, and what is NOT here is worth stating. The
// server's live-row count — the thing the removed ratio bound compared against —
// is not a field on FinalizeResponse, and adding one is a wire change. So the line
// carries what this collect NAMED and how many rows it holds, which is what the
// operator needs to judge whether the refusal is about the collect or about the
// graph, and the server's own log carries the server's numbers.
func reportDeletionRefusal(
	ctx context.Context, result *collectorwire.CollectResult, req *knowledgev1.FinalizeRequest, reason string,
) {
	if reason == "" {
		return
	}
	named := len(req.GetDeletedFiles()) + len(req.GetDeletedNodeIds())
	slog.Error("collect deletion: the server REFUSED this collect's deletion set — the named rows are STILL in the graph",
		"graph", result.GraphName, "branch", result.CurrentBranch, "reason", reason,
		"named", named, "named_files", len(req.GetDeletedFiles()),
		"named_node_ids", len(req.GetDeletedNodeIds()), "collected_rows", len(result.Nodes),
		"recovery", "nothing was destroyed; re-collect once the reason is resolved")
	collector.RecordDeletionRefusal(ctx, reason, named)
}
