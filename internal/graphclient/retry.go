// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"connectrpc.com/connect"
)

// RetryBackoff is the exponential-backoff schedule consumed by both
// the client-side unary reconnect interceptor and the streaming
// resumption loop in collector/remote. Exporting it as a single source
// of truth so tests can reference the same schedule without a second
// literal.
//
// Cumulative budget: 50ms + 200ms + 1s + 3s = 4.25s total across four
// retries. Balances "tool call survives a brief server restart" against
// "don't hang the user's shell on an irrecoverable outage."
var RetryBackoff = []time.Duration{
	50 * time.Millisecond,
	200 * time.Millisecond,
	1 * time.Second,
	3 * time.Second,
}

// IsRetryableTransportError reports whether err represents a transient
// transport-level failure worth retrying. Returns true for:
//
//   - err is io.EOF or wraps io.EOF (server dropped the connection
//     mid-request — typical of a restart while the client pool still
//     holds the old HTTP/2 connection).
//   - err is a *connect.Error with Code == CodeUnavailable.
//   - err wraps net.ErrClosed (pool closed the conn out from under us).
//   - err is a *net.OpError with Op in {"dial","read","write"} whose
//     wrapped error indicates ECONNREFUSED or ECONNRESET.
//   - err is syscall.ECONNREFUSED or syscall.ECONNRESET directly.
//
// Returns false for application-level errors (InvalidArgument,
// NotFound, ResourceExhausted, etc.), context.Canceled,
// context.DeadlineExceeded — those should surface immediately to the
// caller.
func IsRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Explicitly non-retryable: caller cancelled or request deadlined.
	// Covered by connect.CodeCanceled / connect.CodeDeadlineExceeded
	// too but checking first is faster than connect.CodeOf's
	// type-assert path.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// connect-go surfaces transport drops as CodeUnavailable, some
	// compatible implementations use CodeCanceled/CodeUnknown for
	// abrupt stream termination. We treat only CodeUnavailable as
	// retryable; other codes map to application errors or user
	// cancellation.
	var ce *connect.Error
	if errors.As(err, &ce) {
		if ce.Code() == connect.CodeUnavailable {
			return true
		}
		// Fall through — a connect.Error with a non-unavailable code
		// may still wrap a retryable transport error (rare but
		// possible if the transport bubbled an EOF through the error
		// chain). Don't short-circuit here.
	}

	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		switch opErr.Op {
		case "dial", "read", "write":
			return isRetryableOpErr(opErr.Err)
		}
	}
	return false
}

// isRetryableOpErr inspects the inner error of a *net.OpError for
// the connection-level sentinels we treat as retryable.
func isRetryableOpErr(inner error) bool {
	if inner == nil {
		return false
	}
	if errors.Is(inner, syscall.ECONNREFUSED) || errors.Is(inner, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(inner, io.EOF) || errors.Is(inner, net.ErrClosed) {
		return true
	}
	return false
}
