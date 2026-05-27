// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestVoyageEmbedder_HTTP429IsTransient confirms a 429 from the Voyage API
// surfaces as a typed *llm.LLMError with Transient=true even after the batch
// wrapper wraps with "batch %d: %w" — the worker uses errors.As /
// llm.IsTransient to make the retry decision.
func TestVoyageEmbedder_HTTP429IsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	e := &voyageEmbedder{
		BaseURL: server.URL,
		Model:   "voyage-3",
		APIKey:  "test",
		client:  http.DefaultClient,
	}
	_, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
	require.Error(t, err)

	// errors.As must traverse the "batch %d: %w" wrap.
	var llmErr *llm.LLMError
	require.ErrorAs(t, err, &llmErr, "expected *llm.LLMError, got %T (%v)", err, err)
	assert.True(t, llmErr.Transient, "429 must be transient")
	assert.True(t, llm.IsTransient(err))
}

// TestVoyageEmbedder_HTTP500IsTransient confirms 5xx server errors are
// transient.
func TestVoyageEmbedder_HTTP500IsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	e := &voyageEmbedder{
		BaseURL: server.URL,
		Model:   "voyage-3",
		APIKey:  "test",
		client:  http.DefaultClient,
	}
	_, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
	require.Error(t, err)
	assert.True(t, llm.IsTransient(err), "500 must be transient")
}

// TestVoyageEmbedder_HTTP400IsTerminal confirms non-429 4xx errors are
// terminal (write embed_failure_reason marker).
func TestVoyageEmbedder_HTTP400IsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	e := &voyageEmbedder{
		BaseURL: server.URL,
		Model:   "voyage-3",
		APIKey:  "test",
		client:  http.DefaultClient,
	}
	_, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
	require.Error(t, err)
	assert.False(t, llm.IsTransient(err), "400 must NOT be transient")
}
