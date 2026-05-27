// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
)

// newReconnectInterceptor returns a connect-go Interceptor that
// retries unary RPCs on transient transport-level failures. Streaming
// client calls and streaming handler calls are pass-through —
// streaming resumption lives in collector/remote (chunks upload,
// reindex progress) because it needs run-level state (run_id, batch
// cursor) that isn't visible at the interceptor layer.
//
// Backoff schedule is RetryBackoff (4 attempts, cumulative ~4.25s).
// After the schedule exhausts the interceptor returns the last error
// wrapped with attempt-count context so callers can distinguish
// "immediate failure" from "survived the backoff window but still
// down."
//
// Ctx cancellation aborts retry immediately: we never retry a
// request the caller no longer wants, and we never swallow a
// deadline.
func newReconnectInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return retryUnary(ctx, req, next)
		}
	})
}

// retryUnary is the retry loop extracted from the interceptor closure
// to keep per-function line counts under the 80-line funlen cap and
// to give tests a direct handle.
func retryUnary(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
) (connect.AnyResponse, error) {
	totalAttempts := len(RetryBackoff) + 1
	var lastErr error
	for attempt := range totalAttempts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		resp, err := next(ctx, req)
		if err == nil {
			if attempt > 0 {
				slog.Info("connect unary retry succeeded",
					"attempts", attempt+1,
					"procedure", req.Spec().Procedure)
			}
			return resp, nil
		}
		if !IsRetryableTransportError(err) {
			return nil, err
		}
		lastErr = err
		if attempt == len(RetryBackoff) {
			break
		}
		slog.Info("connect unary retry",
			"attempt", attempt+1,
			"procedure", req.Spec().Procedure,
			"backoff_ms", RetryBackoff[attempt].Milliseconds(),
			"err", err)
		select {
		case <-time.After(RetryBackoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf(
		"connect unary retry exhausted after %d attempts: %w",
		totalAttempts, lastErr)
}
