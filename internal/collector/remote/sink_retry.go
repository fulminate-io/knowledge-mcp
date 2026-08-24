// SPDX-License-Identifier: Apache-2.0

package remote

// Retry machinery for the two collect UPLOAD calls — CollectChunk and Finalize.
// It lives beside sink.go rather than inside it only because sink.go reached the
// repository's per-file line ceiling; this is the same upload path, and the
// chunk loop that drives it is in sink.go.

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// uploadWithRetry runs one content-idempotent collect upload call under the
// shared graphclient.RetryBackoff schedule, retrying two DIFFERENT error
// classes on two DIFFERENT budgets:
//
//   - a retryable transport error (server restart mid-collection, TCP drop)
//     keeps the full backoff window, because those are cheap to re-send and
//     unambiguous about what happened;
//   - an ambiguous intermediary error gets exactly
//     graphclient.AmbiguousUploadRetries extra attempts, because a GENUINE
//     server-side rejection lands in the same connect code bucket as a
//     transient cut. Bounding it at one caps the waste on a real server fault
//     at one extra upload, and the true error still surfaces.
//
// Anything else returns immediately, and a cancelled or deadlined ctx aborts
// promptly regardless of which class the error fell in — neither predicate may
// override the caller's give-up signal.
//
// name is the RPC's name for the exhaustion message only. Callers must pass a
// send closure that is safe to invoke more than once under the same epoch.
func uploadWithRetry[T any](ctx context.Context, name string, send func() (T, error)) (T, error) {
	var zero T
	maxAttempts := len(graphclient.RetryBackoff) + 1
	ambiguousUsed := 0
	shedUsed := 0
	var attemptErr error
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		resp, err := send()
		if err == nil {
			return resp, nil
		}
		// delay is the schedule's by default; only the shed arm overrides it,
		// because only a shed comes with a delay the SERVER chose.
		delay := graphclient.RetryBackoff[min(attempt, len(graphclient.RetryBackoff)-1)]
		switch {
		case graphclient.IsRetryableTransportError(err):
		case graphclient.IsAmbiguousUploadError(err) && ambiguousUsed < graphclient.AmbiguousUploadRetries:
			ambiguousUsed++
		default:
			// THE SHED ARM. A shed is the cheapest failure to retry — the server
			// refused before doing any work and said when to come back — so it
			// honors the server's stated delay rather than the local schedule.
			//
			// THE JITTER IS DESYNCHRONISATION. Every client shed in one burst gets
			// the SAME Retry-After, so an unjittered sleep re-forms them into a herd
			// that wakes together and is shed again.
			d, shed := graphclient.IsBackpressureShedError(err)
			if !shed || shedUsed >= graphclient.ShedRetries {
				return zero, err
			}
			shedUsed++
			delay = d + time.Duration(rand.Int64N(int64(d/graphclient.ShedJitterFraction)+1))
			slog.Info("upload shed by server, backing off",
				"rpc", name, "attempt", attempt+1, "delay", delay)
		}
		attemptErr = err
		if attempt == len(graphclient.RetryBackoff) {
			break
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	return zero, fmt.Errorf("%s exhausted after %d attempts: %w", name, maxAttempts, attemptErr)
}

// collectChunkWithRetry sends one CollectChunk under uploadWithRetry's two
// budgets. Content-idempotent for both nodes and edges: re-sending the same
// chunk under the same epoch re-lands nodes identically through the
// carry-forward upsert + epoch GC, and re-lands edges at most once because the
// server's collect edge-landing path filters duplicates on the FOUR-PART edge
// identity (From, To, Type, Evidence) against the batch and the resident graph
// bundles. That is enforced
// server-side rather than assumed here — the node upsert and the presence-row
// INSERT ... ON CONFLICT DO NOTHING live in apply_txn_collect.go, the edge
// ON CONFLICT ... DO UPDATE in apply_txn_sql.go, and the OSS flavor dedups in
// composite_db_write_batch.go.
func (s *UploadSink) collectChunkWithRetry(ctx context.Context, msg *knowledgev1.CollectChunkRequest) error {
	// Chunk upload is the heaviest ingest phase and is worth telling apart from
	// the finalize that follows it, so it re-stamps a refinement over whatever
	// the caller (the collect tool, or a background collector with no
	// originating tool call) put on ctx.
	ctx = graphclient.WithOperation(ctx, graphclient.OpCollectChunk)
	client, err := s.picker(ctx)
	if err != nil {
		return fmt.Errorf("remote sink: resolve ingest client: %w", err)
	}
	_, err = uploadWithRetry(ctx, "CollectChunk",
		func() (*connect.Response[knowledgev1.CollectChunkResponse], error) {
			return client.CollectChunk(ctx, connect.NewRequest(msg))
		})
	return err
}

// finalizeWithRetry sends Finalize under the same two budgets. Finalize used to
// be the one upload call with no retry at all, which cost whole collects to the
// ambiguous shape the chunk loop already absorbed.
//
// Re-entry is safe at the server: the epoch sweep tombstones rows whose
// collect_epoch differs from this one, so a second run under the SAME epoch
// matches strictly fewer rows — finalize.go says so itself, because its
// existing deadlock retry already re-enters that same closure from one layer in.
//
// Benign residual, stated rather than engineered around: a retried Finalize
// mints a SECOND finalize id server-side. The first is already settled by its
// own release before its response was lost, so it is simply never polled — the
// caller polls the id from the response it actually received.
func finalizeWithRetry(
	ctx context.Context, client knowledgev1connect.IngestServiceClient,
	req *connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	return uploadWithRetry(ctx, "Finalize",
		func() (*connect.Response[knowledgev1.FinalizeResponse], error) {
			return client.Finalize(ctx, req)
		})
}
