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
