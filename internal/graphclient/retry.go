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

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
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
//
// THAT STAYS TRUE OF THIS PREDICATE, and deliberately so, even though a
// ResourceExhausted CARRYING a Retry-After is now retried elsewhere: see
// IsBackpressureShedError for the header-qualified case. Widening THIS predicate
// to cover it would nest an interceptor-level retry under uploadWithRetry's own —
// multiplicative attempts on every shed — and would break segmentdist's
// byte-ceiling halving, which is documented as safe precisely because this
// predicate does not retry ResourceExhausted.
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

// ShedRetries is how many EXTRA attempts a SHED upload buys, on top of the first.
//
// A shed is the cheapest possible failure to retry — the server refused before
// doing any work and told us when to come back — so this budget is larger than
// the ambiguous one, which pays a full re-upload per attempt against a fault that
// may be permanent.
//
// IT IS UNRELATED TO THE SERVER'S ADMISSION WIDTH. This governs how many times ONE
// upload re-sends; the server's floor governs how many finalizes hold the write
// lock at once. Since that floor is enforced by finalize-specific semaphores, a
// shed does not compete with any other RPC for a shared counter. Do not "align"
// these numbers with the server's.
const ShedRetries = 3

// ShedJitterFraction is the jitter divisor: a shed sleeps the server's stated
// delay d plus a random value in [0, d/ShedJitterFraction).
//
// THE JITTER IS DESYNCHRONISATION, NOT FUZZ. Every client shed in the same burst
// is handed the SAME Retry-After, so an unjittered sleep re-synchronizes them into
// a herd that wakes together, lands together and is shed together. Spreading the
// wake-ups is what lets a saturated server drain.
const ShedJitterFraction = 2

// IsBackpressureShedError reports whether err is a DELIBERATE SERVER SHED, and
// returns the delay the server asked us to wait.
//
// IT IS A THIRD CLASS, NOT A WIDENING OF EITHER PREDICATE ABOVE, because a shed is
// neither of the things those describe. It is not a transport blip: the server
// NAMED the condition. It is not ambiguous about whether the work landed: it
// did not, and the server said so before starting.
//
// BOTH CONDITIONS ARE REQUIRED — CodeResourceExhausted AND a positive Retry-After.
// The code alone is not single-meaning in this repo, so the header is what makes
// the predicate precise. Retry-After is set only by the DELIBERATE SHED PATHS —
// the backpressure interceptor and the finalize-admission mapping — so its
// presence on a ResourceExhausted means the server shed this request. That is a
// property of those paths, and it survives a third shed path being added.
//
// The delay is parsed with llm.ParseRetryAfter rather than a local parser, so the
// header's two wire forms (delta-seconds and HTTP-date) are handled in one place.
func IsBackpressureShedError(err error) (time.Duration, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeResourceExhausted {
		return 0, false
	}
	h := ce.Meta()
	// The header's PRESENCE is the second half of the predicate, checked here so
	// the condition is explicit at the site rather than implied by the parser's
	// zero return. Parsing itself stays with llm.ParseRetryAfter — one place that
	// knows the header's two wire forms.
	if h.Get("Retry-After") == "" {
		return 0, false
	}
	d := llm.ParseRetryAfter(h)
	if d <= 0 {
		return 0, false
	}
	return d, true
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
