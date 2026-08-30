// SPDX-License-Identifier: Apache-2.0

// intercept_manage_prune.go — client-side manage(prune) intercept. Prune
// hard-deletes (garbage-collects) tombstoned nodes from a graph; it drives the
// generic GraphClient.Index RPC (op INDEX_OP_PRUNE) over the resolved target
// and renders the pruned count. The server does the store work (enumerate
// tombstones, hard-delete sweep, rebuild + persist) — this handler only lowers
// the args to one IndexRequest, carries the reported ids into the local segment
// corpus, and renders the ack plus any warning the server attached.
//
// Prune is GENERIC across every graph type the server resolves; the only
// validation here is a non-empty graph (no closed allowlist, no implicit
// knowledge default) so the operator names the graph they intend to GC.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleClientPrune drives the Index prune op. It requires a non-empty graph,
// parses the optional `before` cutoff (RFC3339 absolute, else relative window),
// fires ONE Index RPC, and renders the pruned count.
//
// A response carrying a WARNING is a PARTIAL SUCCESS, not a failure: the server
// completed the sweep and is reporting that something after it degraded (today,
// the post-commit persist). It is handled loud-but-nonfatal — the ids propagate
// exactly as on a clean success, because they are just as gone server-side, and
// the server's text is logged and rendered VERBATIM rather than reworded, since
// only the server knows what actually degraded.
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
	if w := resp.GetWarning(); w != "" {
		slog.Warn("manage(prune): the server completed the sweep but reported a degradation",
			"graph", a.Graph, "name", a.Name, "warning", w)
	}
	reEmitErr := propagatePrunedToSegments(ctx, deps, a, resp)
	ack := renderPruneAck(a, resp.GetAffectedCount(), beforeNanos, resp.GetWarning())
	if reEmitErr != nil {
		// The prune COMPLETED server-side; only its propagation into this client's
		// shipped corpus failed. It stays a non-error result for the same reason the
		// server's own degradation warning does — but it is no longer an unqualified
		// "Pruned N", which is what a caller used to receive with the local corpus
		// left carrying every pruned document.
		ack += "\n\n" + segmentReEmitFailureNotice(reEmitErr)
	}
	return textResult(ack)
}

// propagatePrunedToSegments carries a completed HARD prune into this client's
// shipped segment corpus. Unlike a tombstone, a pruned row is gone from the
// server outright: no later delta or tombstone scan can surface it again, so an
// id dropped here stays resident in local search results until a full rebuild.
// That is why the server reports the ids rather than only a count.
//
// A server built before the response carried ids returns an empty list. Doing
// nothing is then exactly the pre-field behavior, which is safe — but silently,
// so a non-zero count with no ids is logged rather than swallowed.
//
// A DEGRADED PRUNE REACHES HERE UNCHANGED, and deliberately so. The server
// persists AFTER it commits the hard deletes; a failure in that persist returns
// PARTIAL SUCCESS — the full id set plus a warning — rather than an error,
// precisely so this function still runs. The rows are gone server-side either
// way, so the propagation a degraded prune needs is identical to the one a
// clean prune needs; the warning is the caller's problem to surface, not a
// reason to skip the work. Branching on it here would recreate the very hole
// the partial-success envelope exists to close.
//
// THE RECORD IS SEEDED BEFORE THE BUCKETS ARE TOUCHED, and that order IS the fix for
// the other window. Seeding after the re-emit leaves a crash between the two ending
// with the ids gone from the live buckets, absent from the record, and alive in every
// shipped blob — so the next L2 import resurrects them permanently, and no scan will
// ever report them again to repair it. Seeding first costs nothing if the re-emit then
// fails: the record marks them dead, which is the safe direction.
//
// THE RETURN IS THE RE-EMIT'S VERDICT, and only that one. The record-write failure
// above stays logged-and-continued because the buckets are still worth clearing
// after it; the RE-EMIT failure is what leaves every pruned document resident in
// the shipped corpus, so it is handed back for the ack to qualify. A prune whose
// server sweep completed is never reported as failed either way.
func propagatePrunedToSegments(ctx context.Context, deps ClientDeps, a manageArgs, resp *knowledgev1.IndexResponse) error {
	pruned := resp.GetPrunedIds()
	if len(pruned) == 0 {
		if affected := resp.GetAffectedCount(); affected > 0 {
			slog.Warn("manage(prune): the server reported pruned rows but no ids — the local segment corpus keeps those documents until a rebuild",
				"graph", a.Graph, "name", a.Name, "affected", affected)
		}
		return nil
	}
	// The SAME target resolution reEmitDeletedFromSegments performs internally, so the
	// record, the stamps and the buckets cannot address different corpora.
	gt, name := deleteSegmentTarget(a.Graph, a.Name)
	if shipper := deps.SegmentShipper(); shipper != nil {
		// `pruned` is exactly this window's own reported deletes, which is what the
		// stamp requires. It precedes the merge so no interleaving can observe an id as
		// tombstoned-without-a-stamp — an unstamped id reads as sequence zero, so any
		// queued write for it, including one that began before the prune, would be
		// reported as a re-creation and rebuilt.
		shipper.NoteDeletedIDs(gt, name, pruned)
		// BEST-EFFORT, NEVER FATAL: a completed prune must not be reported as failed
		// because a local record write failed.
		if _, _, merr := mergeTombstonesIntoRecord(shipper, gt, name, pruned); merr != nil {
			slog.Warn("manage(prune): could not record the pruned ids as deleted — a crash before the re-emit ships would resurrect them from a stale imported blob",
				"graph", a.Graph, "name", a.Name, "ids", len(pruned), "error", merr)
		}
	}
	return reEmitDeletedFromSegments(ctx, deps, a.Graph, a.Name, pruned)
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

// renderPruneAck reports the pruned count + the graph target and cutoff, and
// appends the server's warning when the prune completed in a degraded state.
// The warning text is reproduced VERBATIM: only the server knows what degraded,
// and a reworded summary would strip the detail an operator needs to decide
// whether to act.
func renderPruneAck(a manageArgs, pruned, beforeNanos int64, warning string) string {
	target := a.Graph
	if a.Name != "" {
		target = fmt.Sprintf("%s/%s", a.Graph, a.Name)
	}
	ack := fmt.Sprintf("Pruned %d tombstoned node(s) from %s (all tombstones).", pruned, target)
	if beforeNanos != 0 {
		cutoff := time.Unix(0, beforeNanos).UTC().Format(time.RFC3339)
		ack = fmt.Sprintf("Pruned %d tombstoned node(s) from %s (tombstoned before %s).", pruned, target, cutoff)
	}
	if warning == "" {
		return ack
	}
	return fmt.Sprintf("%s\n\nWARNING from the server — the prune COMPLETED, this is not a failure: %s", ack, warning)
}
