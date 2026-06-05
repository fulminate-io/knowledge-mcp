// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestRateLimitHint classifies the two backend error shapes the pipeline sees:
// gateway connect errors and provider *llm.LLMError, with + without a hint.
func TestRateLimitHint(t *testing.T) {
	// connect 429 → Unavailable, with a Retry-After in the error metadata.
	ce := connect.NewError(connect.CodeUnavailable, fmt.Errorf("Too many requests"))
	ce.Meta().Set("Retry-After", "12")
	ra, ok := rateLimitHint(ce)
	assert.True(t, ok, "CodeUnavailable is a rate limit")
	assert.Equal(t, 12*time.Second, ra, "honors the gateway Retry-After")

	// connect ResourceExhausted without a hint → ok, zero hint.
	ce2 := connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("quota"))
	ra, ok = rateLimitHint(ce2)
	assert.True(t, ok)
	assert.Zero(t, ra)

	// A different connect code is NOT a rate limit.
	_, ok = rateLimitHint(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bad")))
	assert.False(t, ok)

	// provider transient LLMError with a parsed Retry-After.
	ra, ok = rateLimitHint(&llm.LLMError{Transient: true, Reason: "http_429", RetryAfter: 7 * time.Second})
	assert.True(t, ok)
	assert.Equal(t, 7*time.Second, ra)

	// provider terminal LLMError + bare error + nil → not a rate limit.
	_, ok = rateLimitHint(&llm.LLMError{Transient: false, Reason: "http_400"})
	assert.False(t, ok)
	_, ok = rateLimitHint(fmt.Errorf("boom"))
	assert.False(t, ok)
	_, ok = rateLimitHint(nil)
	assert.False(t, ok)
}

// TestFailHintHonorsServerDelay proves a server hint is never undershot (only
// positive jitter) and that a zero hint falls back to bounded exponential.
func TestFailHintHonorsServerDelay(t *testing.T) {
	b := newErrBackoff(500*time.Millisecond, 60*time.Second)

	d := b.failHint(10 * time.Second)
	assert.GreaterOrEqual(t, d, 10*time.Second, "must wait at least the server's stated delay")
	assert.LessOrEqual(t, d, 12*time.Second, "positive jitter is bounded to +20%")

	b2 := newErrBackoff(500*time.Millisecond, 60*time.Second)
	d0 := b2.fail() // hint==0 → exponential first step ≈ base ±20%
	assert.LessOrEqual(t, d0, 700*time.Millisecond)
	assert.GreaterOrEqual(t, d0, 400*time.Millisecond)
}

// scriptedExecClient is a WireClient whose Execute returns a scripted error
// sequence (nil = success), counting invocations — used to drive the writeback
// retry path deterministically.
type scriptedExecClient struct {
	mu        sync.Mutex
	execErrs  []error
	execCount int
}

func (c *scriptedExecClient) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (c *scriptedExecClient) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.execCount
	c.execCount++
	if i < len(c.execErrs) && c.execErrs[i] != nil {
		return nil, c.execErrs[i]
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (c *scriptedExecClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.execCount
}

// TestWritebackRetriesOnRateLimit proves a rate-limited update_batch retries
// (preserving the already-computed summary) instead of discarding it, and that
// a NON-rate-limit error returns immediately (exactly one Execute — the
// load-bearing perf criterion holds on the happy/non-429 path).
func TestWritebackRetriesOnRateLimit(t *testing.T) {
	s := "computed-summary"
	items := []updateBatchItem{{ID: "n1", Summary: &s}}

	t.Run("rate_limit_then_success", func(t *testing.T) {
		rl := connect.NewError(connect.CodeUnavailable, fmt.Errorf("Too many requests"))
		c := &scriptedExecClient{execErrs: []error{rl}} // fail once (no Retry-After → ~500ms), then succeed
		err := writeBatchUpdates(context.Background(), c, kgtypes.GraphCode, "repo", items)
		require.NoError(t, err, "writeback must succeed after retrying the rate limit")
		assert.Equal(t, 2, c.count(), "retried once then wrote — computed work not discarded")
	})

	t.Run("non_rate_limit_returns_immediately", func(t *testing.T) {
		c := &scriptedExecClient{execErrs: []error{fmt.Errorf("schema rejected")}}
		err := writeBatchUpdates(context.Background(), c, kgtypes.GraphCode, "repo", items)
		require.Error(t, err)
		assert.Equal(t, 1, c.count(), "non-rate-limit error is not retried (1 RPC)")
	})

	t.Run("ctx_cancel_during_backoff", func(t *testing.T) {
		rl := connect.NewError(connect.CodeUnavailable, fmt.Errorf("Too many requests"))
		c := &scriptedExecClient{execErrs: []error{rl, rl, rl, rl, rl, rl, rl}}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := writeBatchUpdates(ctx, c, kgtypes.GraphCode, "repo", items)
		require.Error(t, err, "ctx cancel during the backoff wait aborts the retry loop")
	})
}
