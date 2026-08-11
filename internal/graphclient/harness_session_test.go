// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
)

// fakeResolveTranscript swaps the package-level resolution seam for a counting
// closure returning handle, restoring the original on cleanup. The returned
// pointer holds the call count — the assertion that matters here, because the
// cache exists to AVOID calls and an assertion on the returned id alone would
// pass against a helper that resolved every time.
func fakeResolveTranscript(t *testing.T, handle hivemonitor.TranscriptHandle) *int {
	t.Helper()
	calls := 0
	orig := resolveTranscript
	resolveTranscript = func(context.Context, hivemonitor.SessionSnapshot) (hivemonitor.TranscriptHandle, error) {
		calls++
		return handle, nil
	}
	t.Cleanup(func() { resolveTranscript = orig })
	return &calls
}

// TestHarnessSessionID_CachesResolutionAndRetriesUnresolved drives the
// per-session harness resolution cache through the seam: a resolved session
// resolves exactly once, an unresolved one retries when the bound is zero, and
// does not retry inside a large bound.
func TestHarnessSessionID_CachesResolutionAndRetriesUnresolved(t *testing.T) {
	t.Run("resolves_once_then_serves_from_cache", func(t *testing.T) {
		calls := fakeResolveTranscript(t, hivemonitor.TranscriptHandle{
			Path:             "/tmp/harness-abc.jsonl",
			HarnessSessionID: "harness-abc",
			Format:           hivemonitor.FormatClaude,
		})

		h := newTestHTTPServer()
		sess := &httpSession{id: "mcp-cache-1", cwd: "/repo/one", pid: 4242, comm: "claude"}

		first := h.harnessSessionID(context.Background(), sess)
		second := h.harnessSessionID(context.Background(), sess)

		assert.Equal(t, "harness-abc", first)
		assert.Equal(t, "harness-abc", second)
		require.Equal(t, 1, *calls, "a resolved session must resolve exactly once and serve the rest from cache")
	})

	t.Run("unresolved_retries_when_bound_is_zero", func(t *testing.T) {
		calls := fakeResolveTranscript(t, hivemonitor.TranscriptHandle{})

		h := newTestHTTPServer()
		h.harnessRetry = 0 // zero is no wait — retry on every call
		sess := &httpSession{id: "mcp-retry-2", cwd: "/repo/two", pid: 5353, comm: "claude"}

		assert.Empty(t, h.harnessSessionID(context.Background(), sess))
		assert.Empty(t, h.harnessSessionID(context.Background(), sess))
		require.Equal(t, 2, *calls, "a zero retry bound must attempt resolution on every call")
	})

	t.Run("unresolved_does_not_retry_within_bound", func(t *testing.T) {
		calls := fakeResolveTranscript(t, hivemonitor.TranscriptHandle{})

		h := newTestHTTPServer()
		h.harnessRetry = time.Hour
		sess := &httpSession{id: "mcp-bound-3", cwd: "/repo/three", pid: 6464, comm: "codex"}

		assert.Empty(t, h.harnessSessionID(context.Background(), sess))
		assert.Empty(t, h.harnessSessionID(context.Background(), sess))
		require.Equal(t, 1, *calls, "a large retry bound must suppress the second attempt")
	})
}
