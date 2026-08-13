// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
)

// TestIsRetryableTransportError covers each retryable branch and the
// key non-retryable cases. Table-driven so adding a new error shape
// is a one-line change.
func TestIsRetryableTransportError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    bool
		message string
	}{
		{"nil", nil, false, "nil is not an error"},
		{"io.EOF", io.EOF, true, "server dropped connection"},
		{"wrapped io.EOF", fmt.Errorf("outer: %w", io.EOF), true, "wrapping preserves retryability"},
		{
			"connect CodeUnavailable",
			connect.NewError(connect.CodeUnavailable, errors.New("service unavailable")),
			true,
			"connect maps transport drops to CodeUnavailable",
		},
		{
			"connect CodeInvalidArgument",
			connect.NewError(connect.CodeInvalidArgument, errors.New("bad input")),
			false,
			"application-level errors never retry",
		},
		{
			"connect CodeNotFound",
			connect.NewError(connect.CodeNotFound, errors.New("not found")),
			false,
			"application-level errors never retry",
		},
		{
			"connect CodeResourceExhausted",
			connect.NewError(connect.CodeResourceExhausted, errors.New("quota")),
			false,
			"license / quota errors surface immediately",
		},
		{"net.ErrClosed", net.ErrClosed, true, "pool closed the conn"},
		{"wrapped net.ErrClosed", fmt.Errorf("outer: %w", net.ErrClosed), true, "wrapping preserves retryability"},
		{"syscall.ECONNREFUSED", syscall.ECONNREFUSED, true, "server not listening"},
		{"syscall.ECONNRESET", syscall.ECONNRESET, true, "connection reset"},
		{
			"OpError dial ECONNREFUSED",
			&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			true,
			"dial to dead server",
		},
		{
			"OpError read ECONNRESET",
			&net.OpError{Op: "read", Err: syscall.ECONNRESET},
			true,
			"server killed mid-read",
		},
		{
			"OpError write EOF",
			&net.OpError{Op: "write", Err: io.EOF},
			true,
			"server died mid-write",
		},
		{
			"OpError accept with non-transport inner",
			&net.OpError{Op: "accept", Err: errors.New("policy denied")},
			false,
			"accept with non-transport inner is not retryable",
		},
		{"context.Canceled", context.Canceled, false, "caller cancelled"},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false, "request deadline fired"},
		{"arbitrary error", errors.New("some app error"), false, "plain error is application-level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsRetryableTransportError(tc.err), tc.message)
		})
	}
}

// TestIsAmbiguousUploadError covers the ambiguous-intermediary class the
// collect upload path re-sends once, and — just as importantly — the shapes it
// must NEVER re-send: an auth denial, a caller cancellation, a request
// deadline, and the CodeUnavailable case that belongs to the transport
// predicate instead. Keeping the two predicates disjoint is what makes their
// two budgets separable.
func TestIsAmbiguousUploadError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    bool
		message string
	}{
		{"nil", nil, false, "nil is not an error"},
		{
			"connect CodeInternal",
			connect.NewError(connect.CodeInternal, errors.New("400 Bad Request")),
			true,
			"a raw 400 from a fronting proxy lands here, as does an h2 RST_STREAM",
		},
		{
			"connect CodeUnknown",
			connect.NewError(connect.CodeUnknown, errors.New("524")),
			true,
			"an unmapped upstream status (520, 524, 408, 413, 499) lands here",
		},
		{
			"wrapped connect CodeInternal",
			fmt.Errorf("remote sink: CollectChunk 1/3: %w",
				connect.NewError(connect.CodeInternal, errors.New("400 Bad Request"))),
			true,
			"wrapping preserves the classification",
		},
		{
			"connect CodeInvalidArgument",
			connect.NewError(connect.CodeInvalidArgument, errors.New("bad input")),
			false,
			"application-level errors never retry",
		},
		{
			"connect CodeUnavailable",
			connect.NewError(connect.CodeUnavailable, errors.New("service unavailable")),
			false,
			"CodeUnavailable is the TRANSPORT predicate's job; the two stay disjoint",
		},
		{
			"connect CodePermissionDenied",
			connect.NewError(connect.CodePermissionDenied, errors.New("permission denied")),
			false,
			"the observed auth shape — re-sending can only produce a slower identical denial",
		},
		{
			"connect CodeDeadlineExceeded",
			connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline")),
			false,
			"a deadline is a give-up signal, never a re-send signal",
		},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false, "request deadline fired"},
		{"context.Canceled", context.Canceled, false, "caller cancelled"},
		{
			"connect CodeInternal wrapping context.Canceled",
			connect.NewError(connect.CodeInternal, context.Canceled),
			false,
			"cancellation is checked FIRST, so it wins over the ambiguous code",
		},
		{"arbitrary error", errors.New("some app error"), false, "plain error is not a connect status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsAmbiguousUploadError(tc.err), tc.message)
		})
	}
}

// TestAmbiguousUploadRetriesBudget pins the budget at 1. The same CodeInternal
// bucket holds both the transient cut and a genuine server fault, so the
// constant caps the waste on a real server error at ONE extra upload.
func TestAmbiguousUploadRetriesBudget(t *testing.T) {
	assert.Equal(t, 1, AmbiguousUploadRetries,
		"one extra upload against a genuine server-side error, not four")
}

// TestRetryBackoff asserts the schedule shape for the interceptor and
// the resume helper.
func TestRetryBackoff(t *testing.T) {
	assert.Len(t, RetryBackoff, 4, "4 attempts per interceptor spec")
	// Monotonic-increasing schedule — ensures backoff isn't
	// accidentally reordered.
	for i := 1; i < len(RetryBackoff); i++ {
		assert.Greater(t, RetryBackoff[i], RetryBackoff[i-1],
			"RetryBackoff must be monotonically increasing")
	}
}
