// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// driveObserver runs one call through the observer INTERCEPTOR (not the helper
// it delegates to) over a handler returning resp/err, and returns what the
// caller sees. Going through WrapUnary is deliberate: a direct helper call would
// pass even if the interceptor never read the response or swallowed the error.
func driveObserver(
	t *testing.T,
	sink *atomic.Uint64,
	resp connect.AnyResponse,
	handlerErr error,
) (connect.AnyResponse, error) {
	t.Helper()
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return resp, handlerErr
	}
	wrapped := newFreshnessObserver(sink).WrapUnary(next)
	return wrapped(context.Background(), connect.NewRequest(&knowledgev1.StatsRequest{}))
}

// TestFreshnessObserverNonZeroOnly pins the recording rule: a non-zero watermark
// is recorded, and none of the three ways a response can carry no watermark may
// clobber it — a later 0, a message that does not declare the field at all, or a
// failed call. The first subtest is the known positive that keeps the other
// three from being vacuously green: without it, an observer that recorded
// NOTHING would satisfy them all.
func TestFreshnessObserverNonZeroOnly(t *testing.T) {
	sink := &atomic.Uint64{}

	t.Run("non_zero_is_recorded", func(t *testing.T) {
		_, err := driveObserver(t, sink, connect.NewResponse(&knowledgev1.StatsResponse{FreshnessGen: 7}), nil)
		require.NoError(t, err)
		assert.Equal(t, uint64(7), sink.Load(), "a non-zero watermark on a successful response is recorded")
	})

	t.Run("later_zero_does_not_overwrite", func(t *testing.T) {
		// 0 is "no watermark", never a value to compare against: recording it
		// would make a cold process's first response look like movement away
		// from a real value.
		_, err := driveObserver(t, sink, connect.NewResponse(&knowledgev1.StatsResponse{FreshnessGen: 0}), nil)
		require.NoError(t, err)
		assert.Equal(t, uint64(7), sink.Load(), "a zero must not overwrite an observed watermark")
	})

	t.Run("field_less_message_is_a_no_op", func(t *testing.T) {
		// HealthCheckResponse declares no freshness_gen at all — the uncovered
		// case the by-name descriptor lookup has to survive without panicking.
		_, err := driveObserver(t, sink, connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil)
		require.NoError(t, err)
		assert.Equal(t, uint64(7), sink.Load(), "a message without the field must leave the sink untouched")
	})

	t.Run("handler_error_passes_through_unrecorded", func(t *testing.T) {
		errSink := &atomic.Uint64{}

		// Known positive on THIS sink first, so the zero asserted below cannot
		// be an observer that simply never records.
		_, err := driveObserver(t, errSink, connect.NewResponse(&knowledgev1.StatsResponse{FreshnessGen: 9}), nil)
		require.NoError(t, err)
		require.Equal(t, uint64(9), errSink.Load())

		// The failed call carries a watermark the observer must not read: the
		// call failed, so nothing about it is evidence of freshness.
		boom := errors.New("handler exploded")
		resp, err := driveObserver(t, errSink,
			connect.NewResponse(&knowledgev1.StatsResponse{FreshnessGen: 11}), boom)
		require.ErrorIs(t, err, boom, "the handler error must reach the caller unaltered")
		assert.NotNil(t, resp, "the handler's own return value passes through untouched")
		assert.Equal(t, uint64(9), errSink.Load(), "a failed call must not record its watermark")
	})
}
