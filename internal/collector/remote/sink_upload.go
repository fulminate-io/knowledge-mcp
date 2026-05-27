// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// uploadChunks sends every batch to the server and returns the
// index-aligned hash slice. Wraps uploadChunksOnce in a retry loop:
// on IsRetryableTransportError (server restart mid-upload, TCP drop),
// closes the current stream, waits per RetryBackoff, redials, and
// resumes from the last un-acked batch. The server dedups on content
// hash so even a re-sent batch is idempotent, but tracking nextIdx
// avoids wasted bandwidth.
func (s *UploadSink) uploadChunks(ctx context.Context, nodes []*knowledgev1.Node) ([]string, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	batches, hashes, err := BatchNodes(nodes, DefaultBatchBytes)
	if err != nil {
		return nil, err
	}

	nextIdx := 0
	var attemptErr error
	maxAttempts := len(graphclient.RetryBackoff) + 1
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stream := s.client.UploadChunks(ctx)
		advanced, err := s.uploadChunksOnce(stream, batches, nextIdx)
		nextIdx = advanced
		if err == nil {
			return hashes, nil
		}
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		if !graphclient.IsRetryableTransportError(err) {
			return nil, err
		}
		attemptErr = err
		if attempt == len(graphclient.RetryBackoff) {
			break
		}
		slog.Info("remote sink: UploadChunks transport drop, redialing",
			"attempt", attempt+1,
			"next_batch_idx", nextIdx,
			"total_batches", len(batches),
			"err", err)
		select {
		case <-time.After(graphclient.RetryBackoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("uploadChunks exhausted after %d attempts: %w",
		maxAttempts, attemptErr)
}

// uploadChunksOnce sends batches starting at startIdx and returns the
// index of the next-un-sent batch on return. On nil error the full
// slice was sent + acked + trailing acks drained; callers short-
// circuit when it returns startIdx == len(batches). On non-nil error
// callers decide whether to retry based on IsRetryableTransportError.
//
// The legacy ChunkAck flow-control / progress signals were
// removed in FUL-251's pre-launch hard-cut pass — the server no longer
// emits them and the wire fields are gone from the proto. Acks now
// carry only accepted_hashes + already_have_hashes.
func (s *UploadSink) uploadChunksOnce(
	stream *connect.BidiStreamForClient[knowledgev1.ChunkBatch, knowledgev1.ChunkAck],
	batches []*knowledgev1.ChunkBatch,
	startIdx int,
) (int, error) {
	i := startIdx
	for ; i < len(batches); i++ {
		b := batches[i]
		if serr := stream.Send(b); serr != nil {
			return i, fmt.Errorf("send batch: %w", serr)
		}
		if _, rerr := stream.Receive(); rerr != nil {
			return i, fmt.Errorf("receive ack: %w", rerr)
		}
	}
	if err := stream.CloseRequest(); err != nil {
		return i, fmt.Errorf("close request: %w", err)
	}
	// Drain trailing acks — io.EOF signals clean end-of-stream.
	for {
		_, rerr := stream.Receive()
		if rerr == nil {
			continue
		}
		if isStreamEOF(rerr) {
			return len(batches), nil
		}
		return i, fmt.Errorf("drain acks: %w", rerr)
	}
}

// isStreamEOF reports whether err represents a clean end-of-stream, either
// io.EOF directly or the connect-wrapped equivalent.
func isStreamEOF(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	// Connect wraps io.EOF in a connect.Error with CodeUnknown; check for
	// that by unwrapping.
	var ce *connect.Error
	if errors.As(err, &ce) {
		if ce.Unwrap() == io.EOF {
			return true
		}
	}
	return errors.Is(err, io.EOF)
}
