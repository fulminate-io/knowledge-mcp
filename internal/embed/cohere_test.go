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

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// cohereServer records every decoded request and answers with one vector
// per text keyed by the REQUESTED embedding type, whose first byte is the
// text's first byte so ordering across packs is assertable.
func cohereServer(t *testing.T, reqs *[]cohereEmbedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// assert, not require: this runs on the server's goroutine, and
		// require's FailNow is only valid on the test goroutine.
		var req cohereEmbedRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.Equal(t, "/v2/embed", r.URL.Path)
		*reqs = append(*reqs, req)
		rows := make([][]int, 0, len(req.Texts))
		for _, text := range req.Texts {
			rows = append(rows, []int{int(text[0])})
		}
		key := "ubinary"
		if len(req.EmbeddingTypes) == 1 {
			key = req.EmbeddingTypes[0]
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": map[string][][]int{key: rows},
		}))
	}))
}

// TestCohereArm_RequestShapeAndBatchCap pins all three divergences the
// typed arm exists to carry — the LIST-valued embedding_types, Cohere's
// own role spelling, and the 96-item cap — plus the keyed response decode.
func TestCohereArm_RequestShapeAndBatchCap(t *testing.T) {
	ctx := context.Background()
	var reqs []cohereEmbedRequest
	server := cohereServer(t, &reqs)
	defer server.Close()

	e, err := NewEmbedder(ctx, &Config{
		Provider: ProviderCohere, APIKey: "k", BaseURL: server.URL,
		Dimension: 256, Dtype: "ubinary", InputRole: InputRoleDocument,
	})
	require.NoError(t, err)

	vec, err := e.EmbedBinary(ctx, "alpha")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"ubinary"}, reqs[0].EmbeddingTypes, "embedding_types is a LIST carrying exactly the mapped dtype")
	assert.Equal(t, "search_document", reqs[0].InputType, "the document role must use COHERE's spelling, not Voyage's")
	assert.Equal(t, cohereDefaultModel, reqs[0].Model)
	assert.Equal(t, 256, reqs[0].OutputDim)
	require.Len(t, vec, 1)
	assert.Equal(t, byte('a'), vec[0], "the decode must read the requested-type key out of the response")

	// The QUERY role posts a DIFFERENT spelling — asserted as a pair so a
	// hardcoded value cannot pass.
	reqs = nil
	q, err := NewEmbedder(ctx, &Config{
		Provider: ProviderCohere, APIKey: "k", BaseURL: server.URL,
		Dimension: 256, Dtype: "ubinary", InputRole: InputRoleQuery,
	})
	require.NoError(t, err)
	_, err = q.EmbedBinary(ctx, "alpha")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Equal(t, "search_query", reqs[0].InputType)
	assert.NotEqual(t, "search_document", reqs[0].InputType, "the two roles must post DISTINCT input_type values")

	// THE 96-ITEM CAP, exercised at the boundary: 97 texts must split into
	// a 96 and a 1, in order.
	reqs = nil
	texts := make([]string, cohereBatchSize+1)
	for i := range texts {
		texts[i] = string(rune('a'+i%26)) + "x"
	}
	vecs, err := e.EmbedBinaryBatch(ctx, texts)
	require.NoError(t, err)
	require.Len(t, vecs, len(texts))
	require.Len(t, reqs, 2, "97 texts at a cap of 96 must be two calls")
	assert.Len(t, reqs[0].Texts, 96)
	assert.Len(t, reqs[1].Texts, 1)
	for i, want := range texts {
		assert.Equal(t, want[0], vecs[i][0], "order must survive packing at item %d", i)
	}
	assert.Equal(t, 96, cohereBatchSize, "the cap is the vendor-documented 96")
}

// TestCohereArm_RefusesEveryDtypeButUbinary pins this arm's capability gate:
// it serves the QUANTIZED representation only, and says so at construction
// rather than at the first embed.
//
// WHAT IT WOULD CATCH, and did. The factory had no dtype gate of any kind, so
// dtype float32 — a value config.AcceptedEmbedDtypes admits — constructed
// successfully, logged a ready embedder, and became a graph's recorded embed
// identity, after which every embed call failed: the arm puts the resolved
// config string on the wire as embedding_types and reads it back as the
// response map key, and cohereEmbedResponse decodes into map[string][][]int,
// which cannot hold an unquantized answer under any spelling.
//
// THE MECHANICAL REASON IS EXECUTED HERE, NOT ONLY ASSERTED IN PROSE, by the
// decode subtest below: the refusal's stated cause is a claim about this
// package's own types, so it is checked rather than told.
//
// The ubinary case is the known-positive control for the refusals — the same
// factory still accepts it, so a red above is the dtype gate firing rather
// than a factory that rejects everything.
func TestCohereArm_RefusesEveryDtypeButUbinary(t *testing.T) {
	ctx := context.Background()

	// "float32" is the admitted config value this gate exists for. The rest
	// are near-miss spellings an operator or a later arm might reach for; none
	// can be decoded by this arm either.
	for _, dtype := range []string{"float32", "float", "int8", "binary", ""} {
		t.Run("refuses "+dtype, func(t *testing.T) {
			_, err := newCohereFromConfig(ctx, &Config{
				Provider: ProviderCohere, APIKey: "k", Dimension: 256, Dtype: dtype,
			})
			require.Error(t, err, "dtype %q must be refused at construction", dtype)
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), cohereDtypeUbinary,
				"the refusal must name the dtype this arm CAN serve, or the operator is left guessing the spelling")
			if dtype != "" {
				assert.Contains(t, err.Error(), dtype, "the refusal must name the configured dtype")
			}
		})
	}

	// Control: ubinary constructs, and the arm carries the representation it
	// agreed to serve so its methods can re-check it.
	e, err := newCohereFromConfig(ctx, &Config{
		Provider: ProviderCohere, APIKey: "k", Dimension: 256, Dtype: cohereDtypeUbinary,
	})
	require.NoError(t, err, "dtype ubinary must be accepted")
	arm := e.(*cohereEmbedder)
	assert.Equal(t, cohereDefaultBaseURL, arm.BaseURL)
	assert.Equal(t, cohereDefaultModel, arm.Model)
	assert.Equal(t, cohereDtypeUbinary, arm.Dtype)

	// The methods re-check, because a test — or any caller inside this
	// package — can build the struct directly and never pass the factory.
	direct := &cohereEmbedder{APIKey: "k", BaseURL: "http://127.0.0.1:1", Model: cohereDefaultModel, Dimension: 256, Dtype: "float32", client: http.DefaultClient}
	_, derr := direct.EmbedBinaryBatch(ctx, []string{"alpha"})
	require.Error(t, derr, "a directly-built arm must not embed at a dtype it cannot decode")
	require.ErrorIs(t, derr, ErrInvalidConfig)

	// THE MECHANICAL REASON, executed: an unquantized response is decimals,
	// and this arm's response type holds ints. Both halves run, so "cannot
	// decode" is not satisfied by a decode that rejects everything.
	t.Run("an unquantized response cannot decode into this arm's response type", func(t *testing.T) {
		var unquantized cohereEmbedResponse
		require.Error(t,
			json.Unmarshal([]byte(`{"embeddings":{"float32":[[0.5,-0.25]]}}`), &unquantized),
			"decimals must fail to unmarshal into map[string][][]int — that failure IS the reason the factory refuses float32")

		var quantized cohereEmbedResponse
		require.NoError(t,
			json.Unmarshal([]byte(`{"embeddings":{"ubinary":[[128,255]]}}`), &quantized),
			"the same type must accept a quantized response, or the assertion above proves nothing")
		assert.Equal(t, [][]int{{128, 255}}, quantized.Embeddings[cohereDtypeUbinary])
	})
}

// TestCohereArm_RefusalDoesNotReachTheIdentityResolver pins the blast radius of
// the dtype gate above: ResolveIdentity gates on Config.Validate, NOT on the
// arm's factory, so a shape this build ADMITS still resolves an identity even
// when the arm refuses to be built at it — the state gemini and
// openai-compatible are already in at ubinary, and cohere now joins at float32.
//
// It matters because the identity is what a graph RECORDS, and the two answers
// are derived by different code: a gate added in a factory must not silently
// change what defaultModelFor reports, or a caller that resolves an identity
// before building an embedder would start recording a different model.
func TestCohereArm_RefusalDoesNotReachTheIdentityResolver(t *testing.T) {
	cfg := &Config{
		Provider: ProviderCohere, APIKey: "k",
		Dimension: config.AcceptedEmbedDimension, Dtype: "float32",
	}

	// The arm refuses to be BUILT at this shape...
	_, berr := NewEmbedder(context.Background(), cfg)
	require.ErrorIs(t, berr, ErrInvalidConfig,
		"the fixture must be a shape the arm refuses, or this test compares nothing")

	// ...and the identity is still stated, at the model the arm would send.
	id, err := ResolveIdentity(cfg)
	require.NoError(t, err, "an admitted shape must still resolve an identity")
	assert.Equal(t, cohereDefaultModel, id.Model)
	assert.Equal(t, ProviderCohere, id.Provider)
	assert.Equal(t, "float32", id.Dtype)

	// Known-positive for the refusal side, same run: a shape the BUILD refuses
	// still has no identity, so "resolves anyway" is not satisfied by a
	// resolver that answers everything.
	_, rerr := ResolveIdentity(&Config{
		Provider: ProviderCohere, APIKey: "k",
		Dimension: config.AcceptedEmbedDimension, Dtype: "float64",
	})
	require.ErrorIs(t, rerr, ErrInvalidConfig)
}

// TestCohereArm_ErrorTaxonomy pins the shared *llm.LLMError vocabulary and
// the transient/terminal split the pipeline's retry decision reads.
func TestCohereArm_ErrorTaxonomy(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		status    int
		transient bool
	}{{http.StatusTooManyRequests, true}, {http.StatusInternalServerError, true}, {http.StatusBadRequest, false}} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		e, err := NewEmbedder(ctx, &Config{
			Provider: ProviderCohere, APIKey: "k", BaseURL: bad.URL, Dimension: 256, Dtype: "ubinary",
		})
		require.NoError(t, err)
		_, err = e.EmbedBinaryBatch(ctx, []string{"x"})
		require.Error(t, err)
		var llmErr *llm.LLMError
		require.ErrorAs(t, err, &llmErr, "errors.As must traverse the batch wrap; got %T", err)
		assert.Equal(t, tc.transient, llmErr.Transient, "status %d", tc.status)
		assert.True(t, strings.HasPrefix(llmErr.Reason, "http_"), "reason %q must carry http_<code>", llmErr.Reason)
		bad.Close()
	}

	// A dead endpoint is transient (retry next tick), not terminal.
	e, err := NewEmbedder(ctx, &Config{
		Provider: ProviderCohere, APIKey: "k", BaseURL: "http://127.0.0.1:1", Dimension: 256, Dtype: "ubinary",
	})
	require.NoError(t, err)
	_, err = e.EmbedBinaryBatch(ctx, []string{"x"})
	require.Error(t, err)
	var llmErr *llm.LLMError
	require.ErrorAs(t, err, &llmErr)
	assert.Equal(t, "network", llmErr.Reason)
	assert.True(t, llm.IsTransient(err))

	// A 200 whose body lacks the requested key is a DECODE failure naming
	// the key — terminal, because retrying an agreed-shape mismatch cannot
	// help.
	wrongKey := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": map[string][][]int{"float": {{1}}},
		}))
	}))
	defer wrongKey.Close()
	e2, err := NewEmbedder(ctx, &Config{
		Provider: ProviderCohere, APIKey: "k", BaseURL: wrongKey.URL, Dimension: 256, Dtype: "ubinary",
	})
	require.NoError(t, err)
	_, err = e2.EmbedBinaryBatch(ctx, []string{"x"})
	require.Error(t, err)
	require.ErrorAs(t, err, &llmErr)
	assert.Equal(t, "decode_response", llmErr.Reason)
	assert.False(t, llmErr.Transient)
	assert.Contains(t, err.Error(), "ubinary", "the decode failure must name the key it looked for")
}
