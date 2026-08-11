// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// echoServer answers every embeddings request with one vector per input whose
// first byte is the input's first byte, so tests can assert per-text identity
// and ordering across packs. It records the input length of every request.
func echoServer(t *testing.T, requestSizes *[]int, failFn func(inputs []string) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req voyageEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			assert.NoError(t, err, "decode embeddings request")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		*requestSizes = append(*requestSizes, len(req.Input))
		if failFn != nil {
			if status, body := failFn(req.Input); status != 0 {
				w.WriteHeader(status)
				w.Write([]byte(body))
				return
			}
		}
		type datum struct {
			Embedding []int `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for _, text := range req.Input {
			out.Data = append(out.Data, datum{Embedding: []int{int(text[0])}})
		}
		assert.NoError(t, json.NewEncoder(w).Encode(out), "encode embeddings response")
	}))
}

// TestVoyageEmbedder_PacksByTokenBudget confirms large texts split into
// multiple API calls under the estimated token budget rather than riding one
// oversized batch, with results preserving input order across packs.
func TestVoyageEmbedder_PacksByTokenBudget(t *testing.T) {
	var sizes []int
	server := echoServer(t, &sizes, nil)
	defer server.Close()

	e := &voyageEmbedder{BaseURL: server.URL, Model: "voyage-3", APIKey: "test", client: http.DefaultClient}

	// Three texts estimating ~40k tokens each: two fit under the 100k budget,
	// the third starts a second pack.
	big := charsPerToken * 40_000
	texts := []string{
		"a" + strings.Repeat("x", big),
		"b" + strings.Repeat("x", big),
		"c" + strings.Repeat("x", big),
	}
	vecs, err := e.EmbedBinaryBatch(context.Background(), texts)
	require.NoError(t, err)
	require.Equal(t, []int{2, 1}, sizes, "expected packs of 2 then 1 under the token budget")
	require.Len(t, vecs, 3)
	assert.Equal(t, byte('a'), vecs[0][0])
	assert.Equal(t, byte('b'), vecs[1][0])
	assert.Equal(t, byte('c'), vecs[2][0])
}

// TestVoyageEmbedder_BisectsOnBatchTokenOverflow confirms a whole-batch token
// rejection splits the pack and retries the halves instead of failing every
// text: the server refuses any multi-text request with Voyage's overflow code
// and accepts singles, and all texts still embed, in order.
func TestVoyageEmbedder_BisectsOnBatchTokenOverflow(t *testing.T) {
	var sizes []int
	overflow := `{"detail":"too many","error_code":"TOO_MANY_TOKENS_IN_BATCH"}`
	server := echoServer(t, &sizes, func(inputs []string) (int, string) {
		if len(inputs) > 1 {
			return http.StatusBadRequest, overflow
		}
		return 0, ""
	})
	defer server.Close()

	e := &voyageEmbedder{BaseURL: server.URL, Model: "voyage-3", APIKey: "test", client: http.DefaultClient}

	vecs, err := e.EmbedBinaryBatch(context.Background(), []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Len(t, vecs, 4)
	for i, want := range []byte{'a', 'b', 'c', 'd'} {
		assert.Equal(t, want, vecs[i][0], "order must survive bisection")
	}
	// One refused 4-pack, two refused 2-packs, four accepted singles.
	assert.Equal(t, []int{4, 2, 1, 1, 2, 1, 1}, sizes)
}

// TestVoyageEmbedder_GenericBadRequestDoesNotBisect confirms only the batch
// token-cap rejection triggers bisection; any other 400 fails the pack once,
// with no splitting retries burning API calls on a deterministic error.
func TestVoyageEmbedder_GenericBadRequestDoesNotBisect(t *testing.T) {
	var sizes []int
	server := echoServer(t, &sizes, func([]string) (int, string) {
		return http.StatusBadRequest, `{"detail":"malformed input"}`
	})
	defer server.Close()

	e := &voyageEmbedder{BaseURL: server.URL, Model: "voyage-3", APIKey: "test", client: http.DefaultClient}

	_, err := e.EmbedBinaryBatch(context.Background(), []string{"a", "b", "c"})
	require.Error(t, err)
	assert.False(t, llm.IsTransient(err))
	assert.Equal(t, []int{3}, sizes, "a generic 400 must not trigger bisection")
}
