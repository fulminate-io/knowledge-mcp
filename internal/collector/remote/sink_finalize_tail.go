// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// sink_finalize_tail.go — the client's wait on the server's DETACHED finalize
// tail. Split out of sink.go, which had grown past the repo's file-length cap;
// the poller has no dependency on WriteResult's frame beyond its arguments, so
// the move is a relocation rather than a refactor.

// finalizeTailPoll is the interval between FinalizeStatus polls, and
// finalizeTailWait the total budget before the collect stops waiting.
//
// The budget is deliberately LONGER than the edge timeout the detached tail
// exists to escape: the point of moving the tail off the response path was never
// to make the work faster, only to stop a slow tail from failing the REQUEST. A
// budget shorter than the tail's realistic duration would reintroduce exactly the
// impatience that was moved out of the transport.
const (
	finalizeTailPoll = 2 * time.Second
	finalizeTailWait = 10 * time.Minute
)

// awaitFinalizeTail polls FinalizeStatus until the server's detached finalize
// tail settles, so a collect reports what actually happened rather than assuming
// the acknowledgement was the whole story.
//
// NOTHING HERE FAILS THE COLLECT. By the time Finalize returned, the durable half
// — the epoch-tombstone sweep recording this collection's deletions — was already
// committed; the tail is follow-up over that committed state. A failed or
// unfinished tail means some staleness marking or tombstone pruning is owed, which
// the next collect redoes, so turning it into a collect failure would report a
// loss that did not happen. The outcome is logged at a severity matching what it
// means and the collect succeeds.
//
// IT REPORTS THE TERMINAL STATE IT OBSERVED, and that return — not the error — is
// what a caller may treat as success. Every branch below returns a nil error by
// the rule above, so nil says only "the collect stands"; it does not say the tail
// completed. A caller advancing durable state on nil would advance it after a
// FAILED tail. Only FINALIZE_STATE_DONE means the tail finished; UNSPECIFIED is
// returned where NO state was observed at all (no id to poll, or the poll itself
// failed) and RUNNING where the last observed state was still in flight.
//
//nolint:unparam // the error is the intentional named API: every branch returns nil TODAY because nothing here may fail the collect, and keeping the return makes a future decision to fail one a signature change at this seam rather than a silent behavioral flip at the call site.
func awaitFinalizeTail(
	ctx context.Context, client knowledgev1connect.IngestServiceClient,
	finalizeID, graphName string, finStart time.Time,
) (knowledgev1.FinalizeState, error) {
	if finalizeID == "" {
		// A server too old to return an id. Nothing to poll; the acknowledgement is
		// all the completion signal that exists against it. UNSPECIFIED rather than
		// DONE: this client has NO completion signal here, and a watermark must not
		// advance on an unobserved outcome. The cost is one redundant full upload
		// against an old server, which is the conservative direction.
		slog.Debug("remote sink: finalize done (server returned no finalize id)", "graph", graphName)
		return knowledgev1.FinalizeState_FINALIZE_STATE_UNSPECIFIED, nil
	}
	deadline := time.Now().Add(finalizeTailWait)
	for {
		resp, err := client.FinalizeStatus(graphclient.WithOperation(ctx, graphclient.OpCollectFinalize),
			connect.NewRequest(&knowledgev1.FinalizeStatusRequest{FinalizeId: finalizeID}))
		if err != nil {
			slog.Warn("remote sink: finalize status poll failed — collect stands, tail completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID, "error", err)
			return knowledgev1.FinalizeState_FINALIZE_STATE_UNSPECIFIED, nil
		}
		switch resp.Msg.GetState() {
		case knowledgev1.FinalizeState_FINALIZE_STATE_DONE:
			slog.Debug("remote sink: finalize done", "graph", graphName,
				"dur", time.Since(finStart).Round(time.Millisecond))
			return knowledgev1.FinalizeState_FINALIZE_STATE_DONE, nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_FAILED:
			slog.Error("remote sink: finalize tail FAILED — the collection landed, but its follow-up "+
				"(container staleness marks, tombstone prune) did not complete",
				"graph", graphName, "finalize_id", finalizeID, "error", resp.Msg.GetError())
			return knowledgev1.FinalizeState_FINALIZE_STATE_FAILED, nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN:
			// Finalize ids are process-local, so a poll routed to a different replica
			// than served the Finalize legitimately lands here. Not an error, and not
			// worth retrying — the replica that knows will never be asked again.
			slog.Debug("remote sink: finalize tail status unknown to the serving replica — "+
				"completion unconfirmed", "graph", graphName, "finalize_id", finalizeID)
			return knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN, nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_RUNNING,
			knowledgev1.FinalizeState_FINALIZE_STATE_UNSPECIFIED:
			// Keep waiting.
		}
		if time.Now().After(deadline) {
			slog.Warn("remote sink: finalize tail still running after the wait budget — collect stands, "+
				"tail completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID, "waited", finalizeTailWait)
			return knowledgev1.FinalizeState_FINALIZE_STATE_RUNNING, nil
		}
		select {
		case <-ctx.Done():
			slog.Debug("remote sink: finalize tail wait cancelled — collect stands, completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID)
			return knowledgev1.FinalizeState_FINALIZE_STATE_RUNNING, nil
		case <-time.After(finalizeTailPoll):
		}
	}
}
