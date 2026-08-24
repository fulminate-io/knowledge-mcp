// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// shedErr builds the exact pair the server's two shed paths emit: a
// ResourceExhausted carrying a Retry-After of the given seconds.
func shedErr(seconds string) error {
	e := connect.NewError(connect.CodeResourceExhausted, errors.New("server at capacity"))
	e.Meta().Set("Retry-After", seconds)
	return e
}

// TestShedRetry_JitteredDelayWithinBounds pins the shed arm's two properties: it
// RETRIES a shed up to its budget, and each sleep is the server's stated delay
// plus jitter strictly inside [d, d + d/ShedJitterFraction].
//
// THE JITTER BOUND IS THE POINT. Too little and every shed client in a burst
// wakes together and re-sheds; unbounded and a client could sleep far past what
// the server asked for. The measured elapsed time is the observable, so the test
// asserts the window rather than the random draw.
//
// A KNOWN-POSITIVE CONTROL RUNS IN THE SAME TEST: a ResourceExhausted with NO
// Retry-After must NOT be retried. Without it, a predicate that returned false for
// everything would satisfy the budget assertion by never retrying at all.
func TestShedRetry_JitteredDelayWithinBounds(t *testing.T) {
	const stated = time.Second

	t.Run("retries_within_budget_and_jitter_window", func(t *testing.T) {
		attempts := 0
		start := time.Now()
		_, err := uploadWithRetry(t.Context(), "Finalize", func() (int, error) {
			attempts++
			if attempts <= graphclient.ShedRetries {
				return 0, shedErr("1")
			}
			return 7, nil
		})
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.Equal(t, graphclient.ShedRetries+1, attempts,
			"a shed must be retried up to its budget and then succeed")

		lo := time.Duration(graphclient.ShedRetries) * stated
		hi := lo + lo/graphclient.ShedJitterFraction
		require.GreaterOrEqual(t, elapsed, lo,
			"slept less than the server's stated delay: the shed arm is not honoring Retry-After")
		require.LessOrEqual(t, elapsed, hi+2*time.Second,
			"slept far past the stated delay plus its jitter ceiling")
	})

	t.Run("control_resource_exhausted_without_header_is_not_retried", func(t *testing.T) {
		attempts := 0
		_, err := uploadWithRetry(t.Context(), "Finalize", func() (int, error) {
			attempts++
			return 0, connect.NewError(connect.CodeResourceExhausted, errors.New("permanent refusal"))
		})
		require.Error(t, err)
		require.Equal(t, 1, attempts,
			"control: a ResourceExhausted with NO Retry-After is not a shed and must surface immediately")
	})
}
