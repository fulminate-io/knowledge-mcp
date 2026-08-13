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
	if ce, ok := errors.AsType[*connect.Error](err); ok {
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

	if opErr, ok := errors.AsType[*net.OpError](err); ok {
		switch opErr.Op {
		case "dial", "read", "write":
			return isRetryableOpErr(opErr.Err)
		}
	}
	return false
}

// AmbiguousUploadRetries is how many EXTRA attempts an ambiguous upload error
// buys, on top of the first one. It is deliberately 1 rather than the full
// RetryBackoff window: the same CodeInternal bucket that holds a transient
// intermediary cut also holds a genuine server-side application error (the
// production log carries an "internal: ingest: CollectChunk: ... ERROR:
// relation ..." shape). A budget of 1 caps the waste on that real fault at ONE
// extra multi-megabyte upload rather than four, and still surfaces the true
// error to the caller.
const AmbiguousUploadRetries = 1

// IsAmbiguousUploadError reports whether err is an intermediary error the
// client cannot attribute — one where "the server rejected this" and "something
// between us cut the request" are indistinguishable from the client side.
// True only for a *connect.Error with Code == CodeInternal or CodeUnknown.
//
// Those two codes read reckless to retry until you see where connect-go
// deposits unattributable statuses. Three distinct sources feed them:
//
//   - a raw 400 from a fronting proxy — protocol.go's httpToCode maps 400 to
//     CodeInternal, which is what a body-read deadline cut looks like to us;
//   - an unmapped upstream status such as 520, 524, 408, 413 or 499 — the
//     default arm of that same mapping yields CodeUnknown;
//   - an h2 RST_STREAM. connect's wrapIfRSTError turns EIGHT stream error codes
//     into CodeInternal — NO_ERROR, PROTOCOL_ERROR, INTERNAL_ERROR,
//     FLOW_CONTROL_ERROR, SETTINGS_TIMEOUT, FRAME_SIZE_ERROR,
//     COMPRESSION_ERROR and CONNECT_ERROR — while REFUSED_STREAM becomes
//     Unavailable and CANCEL becomes DeadlineExceeded only when the caller's own
//     deadline has actually elapsed. The breadth is the point: it is what makes
//     CodeInternal the catch-all bucket for stream terminations the client
//     cannot attribute.
//
// Returns false for:
//
//   - context.Canceled and context.DeadlineExceeded, checked FIRST (mirroring
//     IsRetryableTransportError's own ordering). A caller deadline is a give-up
//     signal, never a re-send signal, and the RST_STREAM CANCEL branch above can
//     produce a DeadlineExceeded that must not be re-sent.
//   - CodePermissionDenied — a shape the production log actually contains
//     ("remote sink: CollectChunk N/M: permission_denied"). Re-sending it could
//     only turn into a slower identical denial.
//   - every other code, including CodeUnavailable, which is
//     IsRetryableTransportError's job. Keeping the two predicates disjoint is
//     what lets their two retry budgets stay separable.
//
// BOUNDARY, and do not widen it: this predicate is for the collect UPLOAD path
// only — CollectChunk and Finalize — because those two calls are
// content-idempotent under their shared epoch. It is NOT a general retry rule.
// On any other RPC an unattributable server error is information the caller
// needs immediately, and a blanket retry on this class would mask real client
// bugs behind a slower failure.
func IsAmbiguousUploadError(err error) bool {
	if err == nil {
		return false
	}
	// Checked before the code inspection: a cancelled or deadlined caller must
	// never be re-sent, whichever code it arrives wearing.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code() == connect.CodeInternal || ce.Code() == connect.CodeUnknown
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
