// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestGeminiArm_RefusesNonFloat32GoogleAuth pins the arm's two defining
// behaviors: the refusal of every dtype but the unquantized one, and Google's
// own auth header and task_type vocabulary on the wire.
//
// The float32 case is the known-positive control for the refusals — the same
// factory accepts dtype float32, so a red above is the dtype gate firing
// rather than the factory rejecting everything.
//
// "float" IS IN THE REFUSED LIST DELIBERATELY. It is the literal this arm used
// to gate on, and it is not a value config.AcceptedEmbedDtypes contains, so an
// operator can never write it. Asserting it is refused is what stops the
// config-vocabulary/wire-vocabulary conflation from being reintroduced here.
func TestGeminiArm_RefusesNonFloat32GoogleAuth(t *testing.T) {
	ctx := context.Background()

	for _, dtype := range []string{"ubinary", "binary", "int8", "float", ""} {
		t.Run("refuses "+dtype, func(t *testing.T) {
			_, err := newGeminiFromConfig(ctx, &Config{
				Provider: ProviderGemini, APIKey: "k", Dimension: 256, Dtype: dtype,
			})
			require.Error(t, err, "dtype %q must be refused", dtype)
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), geminiDtypeFloat32,
				"the refusal must name the dtype this arm CAN serve, or the operator is left guessing the spelling")
			if dtype != "" {
				assert.Contains(t, err.Error(), dtype, "the refusal must name the configured dtype")
			}
		})
	}

	// Control: float32 — the config vocabulary's unquantized spelling — is
	// accepted, and the arm defaults its endpoint/model.
	e, err := newGeminiFromConfig(ctx, &Config{
		Provider: ProviderGemini, APIKey: "k", Dimension: 256, Dtype: geminiDtypeFloat32,
	})
	require.NoError(t, err, "dtype float32 must be accepted")
	arm := e.(*geminiEmbedder)
	assert.Equal(t, geminiDefaultBaseURL, arm.BaseURL)
	assert.Equal(t, geminiDefaultModel, arm.Model)
	assert.Equal(t, geminiDtypeFloat32, arm.Dtype,
		"the arm must carry the representation it agreed to serve, or its methods cannot re-check it")

	// The wire: x-goog-api-key (NOT an Authorization Bearer), the
	// :batchEmbedContents path, task_type and outputDimensionality.
	var gotAuth, gotBearer, gotPath string
	var got geminiEmbedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-goog-api-key")
		gotBearer = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		// assert, not require: this runs on the server's goroutine, and
		// require's FailNow is only valid on the test goroutine.
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&got)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": []map[string]any{{"values": []float32{0.5}}},
		}))
	}))
	defer server.Close()

	doc := &geminiEmbedder{APIKey: "k", BaseURL: server.URL, Model: "gemini-embedding-001", Dimension: 256, Dtype: geminiDtypeFloat32, InputRole: InputRoleDocument, client: http.DefaultClient}
	vecs, err := doc.embedFloatBatch(ctx, []string{"package main"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	assert.Equal(t, []float32{0.5}, vecs[0])
	assert.Equal(t, "k", gotAuth, "Gemini authenticates with x-goog-api-key")
	assert.Empty(t, gotBearer, "an Authorization Bearer must NOT be sent")
	assert.Equal(t, "/v1beta/models/gemini-embedding-001:batchEmbedContents", gotPath)
	require.Len(t, got.Requests, 1)
	assert.Equal(t, "RETRIEVAL_DOCUMENT", got.Requests[0].TaskType)
	assert.Equal(t, 256, got.Requests[0].OutputDimensionality)
	assert.Equal(t, "package main", got.Requests[0].Content.Parts[0].Text)

	// The QUERY role posts Gemini's OTHER spelling — the pair is what
	// catches a hardcoded value.
	query := &geminiEmbedder{APIKey: "k", BaseURL: server.URL, Model: "gemini-embedding-001", Dimension: 256, InputRole: InputRoleQuery, client: http.DefaultClient}
	_, err = query.embedFloatBatch(ctx, []string{"package main"})
	require.NoError(t, err)
	require.Len(t, got.Requests, 1)
	assert.Equal(t, "RETRIEVAL_QUERY", got.Requests[0].TaskType)
	assert.NotEqual(t, "RETRIEVAL_DOCUMENT", got.Requests[0].TaskType)

	// A keyless base_url sends no key header at all.
	var sawHeader bool
	keyless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["X-Goog-Api-Key"]
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"embeddings": []map[string]any{{"values": []float32{1}}}}))
	}))
	defer keyless.Close()
	local := &geminiEmbedder{BaseURL: keyless.URL, Model: "m", client: http.DefaultClient}
	_, err = local.embedFloatBatch(ctx, []string{"x"})
	require.NoError(t, err)
	assert.False(t, sawHeader, "a keyless endpoint must not be handed an empty key header")

	// A directly-constructed arm carrying a dtype this arm never agreed to
	// serve still refuses at the METHOD, not only at the factory it bypassed.
	// This is the property that stops a hand-built instance from emitting
	// float bytes under a label claiming something else.
	mislabeled := &geminiEmbedder{APIKey: "k", BaseURL: server.URL, Model: "m", Dtype: "ubinary", client: http.DefaultClient}
	_, err = mislabeled.EmbedBinaryBatch(ctx, []string{"x"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidConfig)
	_, err = mislabeled.EmbedBinary(ctx, "x")
	require.Error(t, err, "the single-text path must refuse identically — it delegates to the batch one")

	require.True(t, HasProvider(ProviderGemini), "the arm must self-register from init()")
}

// TestGeminiArm_EmbedBinaryBatchEmitsLittleEndianFloat32 covers the hop that
// used to be a stub: EmbedBinaryBatch is the ONLY method the pipeline calls, so
// an arm whose factory constructs but whose batch path refuses is unconstructed
// in every way that matters. It previously returned the dtype refusal
// unconditionally, and nothing was red because no test drove it expecting bytes.
//
// THE EXPECTATION IS HAND-WRITTEN from the IEEE-754 bit patterns rather than
// produced by the encoder under test. Round-tripping through binary.LittleEndian
// here would agree with itself under either byte order and would pass on a build
// whose segment reader disagrees — which is the failure this exists to catch,
// since a byte-order mismatch yields finite wrong distances rather than an error.
// The values are asymmetric across their four bytes for the same reason; 0.0
// would encode identically under either order and prove nothing.
func TestGeminiArm_EmbedBinaryBatchEmitsLittleEndianFloat32(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": []map[string]any{{"values": []float32{1.0, -2.0, 0.5}}},
		}))
	}))
	defer server.Close()

	arm := &geminiEmbedder{
		APIKey: "k", BaseURL: server.URL, Model: "m", Dimension: 24,
		Dtype: geminiDtypeFloat32, client: http.DefaultClient,
	}
	got, err := arm.EmbedBinaryBatch(ctx, []string{"package main"})
	require.NoError(t, err, "a float32 arm must EMBED rather than return its own capability refusal")
	require.Len(t, got, 1)

	//   1.0 = 0x3F800000 -> 00 00 80 3F little-endian
	//  -2.0 = 0xC0000000 -> 00 00 00 C0
	//   0.5 = 0x3F000000 -> 00 00 00 3F
	require.Equal(t, []byte{
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x00, 0xC0,
		0x00, 0x00, 0x00, 0x3F,
	}, got[0], "float32 values must encode little-endian, four bytes each")
	require.Len(t, got[0], 12, "3 float32 values weigh 12 bytes; one byte each would be 3")

	// EMPTY INPUT IS NOT AN ERROR AND MAKES NO CALL — the pipeline hands empty
	// packs at the tail of a drain.
	empty, err := arm.EmbedBinaryBatch(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestGeminiArm_ErrorTaxonomyAndCap pins the shared *llm.LLMError
// vocabulary and the arm's own named batch cap.
func TestGeminiArm_ErrorTaxonomyAndCap(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		status    int
		transient bool
	}{{http.StatusTooManyRequests, true}, {http.StatusInternalServerError, true}, {http.StatusBadRequest, false}} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		arm := &geminiEmbedder{APIKey: "k", BaseURL: bad.URL, Model: "m", client: http.DefaultClient}
		_, err := arm.embedFloatBatch(ctx, []string{"x"})
		require.Error(t, err)
		var llmErr *llm.LLMError
		require.ErrorAs(t, err, &llmErr, "got %T", err)
		assert.Equal(t, tc.transient, llmErr.Transient, "status %d", tc.status)
		assert.True(t, strings.HasPrefix(llmErr.Reason, "http_"), "reason %q", llmErr.Reason)
		bad.Close()
	}

	dead := &geminiEmbedder{APIKey: "k", BaseURL: "http://127.0.0.1:1", Model: "m", client: http.DefaultClient}
	_, err := dead.embedFloatBatch(ctx, []string{"x"})
	require.Error(t, err)
	var llmErr *llm.LLMError
	require.ErrorAs(t, err, &llmErr)
	assert.Equal(t, "network", llmErr.Reason)
	assert.True(t, llmErr.Transient)

	// The arm's own cap is a real bound and its own number.
	texts := make([]string, geminiBatchSize+2)
	packs := packGemini(texts)
	require.Len(t, packs, 2)
	assert.Len(t, packs[0], geminiBatchSize)
	assert.Len(t, packs[1], 2)
	assert.NotEqual(t, embedBatchSize, geminiBatchSize, "one provider's cap must not govern another's")
}

// TestEmbedRegistry_ListProvidersCoversVocabulary asserts the registry
// holds EXACTLY the five declared vocabulary values once every arm's
// init() has run — no arm missing, none extra.
//
// The expectation is a literal list written from the config vocabulary,
// not derived from the registry, so a dropped arm cannot make both sides
// shrink together.
func TestEmbedRegistry_ListProvidersCoversVocabulary(t *testing.T) {
	want := []Provider{"cohere", "fake", "gemini", "openai-compatible", "voyage"}
	got := ListProviders()
	require.Len(t, got, 5, "ListProviders() = %v; want exactly the five declared values", got)
	assert.Equal(t, want, got, "ListProviders() must return the sorted vocabulary")

	for _, p := range want {
		assert.True(t, HasProvider(p), "provider %q is not registered", p)
		assert.True(t, p.IsValid(), "provider %q is registered but not in the config vocabulary", p)
	}
	// Nothing registered that the vocabulary does not name.
	for _, p := range got {
		assert.True(t, slices.Contains(want, p), "registry carries an undeclared provider %q", p)
	}
}
