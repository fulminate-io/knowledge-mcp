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

// TestOpenAICompatArm_RefusesNonFloat32 asserts the arm serves the unquantized
// representation and refuses every other dtype, naming both the value it
// refused and the one it can serve.
//
// The float32 case is the known-positive control: the same factory accepts
// dtype float32, so the refusals below are the dtype gate firing and not the
// factory rejecting everything.
//
// "float" IS IN THE REFUSED LIST DELIBERATELY. It is this envelope's WIRE
// spelling for encoding_format and is not a value config.AcceptedEmbedDtypes
// contains, so no operator can write it. This arm previously gated its
// CAPABILITY on that same wire literal, which is what made it unconstructible
// under every admitted configuration; asserting it is refused as a dtype is
// what stops the two vocabularies being conflated here again.
func TestOpenAICompatArm_RefusesNonFloat32(t *testing.T) {
	ctx := context.Background()

	for _, dtype := range []string{"ubinary", "binary", "int8", "uint8", "float", ""} {
		t.Run("refuses "+dtype, func(t *testing.T) {
			_, err := newOpenAICompatFromConfig(ctx, &Config{
				Provider: ProviderOpenAICompatible, APIKey: "k", Dimension: 256, Dtype: dtype,
			})
			require.Error(t, err, "dtype %q must be refused", dtype)
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), openAICompatDtypeFloat32,
				"the refusal must name the dtype this arm CAN serve, or the operator is left guessing the spelling")
			if dtype != "" {
				assert.Contains(t, err.Error(), dtype, "the refusal must name the configured dtype")
			}
		})
	}

	// The two vocabularies are DIFFERENT STRINGS, stated as an assertion rather
	// than left to the reader: if a later edit collapses them back to one
	// constant this fails, which is the whole defect this file's arm carried.
	require.NotEqual(t, openAICompatDtypeFloat32, openAICompatWireEncodingFormat,
		"the config dtype vocabulary and the encoding_format wire vocabulary are different sets")

	// Control: float32 — the config vocabulary's unquantized spelling — is
	// accepted and the arm defaults its endpoint/model.
	e, err := newOpenAICompatFromConfig(ctx, &Config{
		Provider: ProviderOpenAICompatible, APIKey: "k", Dimension: 256, Dtype: openAICompatDtypeFloat32,
	})
	require.NoError(t, err, "dtype float32 must be accepted")
	arm := e.(*openAICompatEmbedder)
	assert.Equal(t, openAICompatDefaultBaseURL, arm.BaseURL)
	assert.Equal(t, openAICompatDefaultModel, arm.Model)
	assert.Equal(t, openAICompatDtypeFloat32, arm.Dtype,
		"the arm must carry the representation it agreed to serve, or its methods cannot re-check it")
	assert.True(t, arm.Available())

	// A keyless base_url is a valid configuration for this arm — it is the
	// population it primarily serves.
	keyless, err := newOpenAICompatFromConfig(ctx, &Config{
		Provider: ProviderOpenAICompatible, BaseURL: "http://127.0.0.1:8000", Dimension: 256, Dtype: openAICompatDtypeFloat32,
	})
	require.NoError(t, err)
	assert.True(t, keyless.Available())

	// A directly-constructed arm carrying a dtype it never agreed to serve
	// still refuses at the METHOD, not only at the factory it bypassed.
	mislabeled := &openAICompatEmbedder{APIKey: "k", BaseURL: "http://127.0.0.1:1", Model: "m", Dtype: "ubinary", client: http.DefaultClient}
	_, err = mislabeled.EmbedBinaryBatch(ctx, []string{"x"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidConfig)
	_, err = mislabeled.EmbedBinary(ctx, "x")
	require.Error(t, err, "the single-text path must refuse identically — it delegates to the batch one")

	// The registry reaches the arm under the config vocabulary value, and the
	// arm's dtype gate is the one that refuses ubinary — Validate admits it.
	_, err = NewEmbedder(ctx, &Config{Provider: Provider("openai-compatible"), APIKey: "k", Dimension: 256, Dtype: "ubinary"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidConfig)

	// ... and constructs at the dtype it can serve, which is what makes the
	// refusal above a capability statement rather than an unreachable arm.
	built, err := NewEmbedder(ctx, &Config{
		Provider: Provider("openai-compatible"), APIKey: "k", Dimension: 256, Dtype: openAICompatDtypeFloat32,
	})
	require.NoError(t, err, "the registry must be able to build this arm at some admitted dtype")
	require.NotNil(t, built)

	require.True(t, HasProvider(ProviderOpenAICompatible), "the arm must self-register from init()")
}

// TestOpenAICompatArm_EmbedBinaryBatchEmitsLittleEndianFloat32 covers the hop
// that used to be a stub: EmbedBinaryBatch is the ONLY method the pipeline
// calls, so an arm whose factory constructs but whose batch path refuses is
// unconstructed in every way that matters. It previously returned the dtype
// refusal unconditionally, and nothing was red because no test drove it
// expecting bytes.
//
// THE EXPECTATION IS HAND-WRITTEN from the IEEE-754 bit patterns rather than
// produced by the encoder under test — see the sibling gemini test for why a
// round trip through binary.LittleEndian would prove nothing about byte order.
//
// IT ALSO PINS THAT THE WIRE FIELD DID NOT MOVE. The request still carries
// encoding_format "float" while the arm's dtype is "float32", which is the
// separation the fix exists to establish; asserting only the bytes would let a
// later edit put the config spelling on the wire unnoticed.
func TestOpenAICompatArm_EmbedBinaryBatchEmitsLittleEndianFloat32(t *testing.T) {
	ctx := context.Background()

	var got openAICompatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&got)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1.0, -2.0, 0.5}}},
		}))
	}))
	defer server.Close()

	arm := &openAICompatEmbedder{
		APIKey: "k", BaseURL: server.URL, Model: "m", Dimension: 24,
		Dtype: openAICompatDtypeFloat32, client: http.DefaultClient,
	}
	vecs, err := arm.EmbedBinaryBatch(ctx, []string{"package main"})
	require.NoError(t, err, "a float32 arm must EMBED rather than return its own capability refusal")
	require.Len(t, vecs, 1)

	//   1.0 = 0x3F800000 -> 00 00 80 3F little-endian
	//  -2.0 = 0xC0000000 -> 00 00 00 C0
	//   0.5 = 0x3F000000 -> 00 00 00 3F
	require.Equal(t, []byte{
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x00, 0xC0,
		0x00, 0x00, 0x00, 0x3F,
	}, vecs[0], "float32 values must encode little-endian, four bytes each")
	require.Len(t, vecs[0], 12, "3 float32 values weigh 12 bytes; one byte each would be 3")

	assert.Equal(t, openAICompatWireEncodingFormat, got.EncodingFormat,
		"the WIRE field carries the OpenAI vocabulary, never the config dtype")
	assert.NotEqual(t, arm.Dtype, got.EncodingFormat,
		"and the two are different strings — a request carrying %q would be the conflation this fix removed", arm.Dtype)

	// EMPTY INPUT IS NOT AN ERROR AND MAKES NO CALL — the pipeline hands empty
	// packs at the tail of a drain.
	empty, err := arm.EmbedBinaryBatch(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestOpenAICompatArm_RequestAndErrorTaxonomy exercises the arm's real
// request path: the OpenAI envelope on the wire, a float32 decode, and the
// shared *llm.LLMError reason vocabulary with the transient/terminal split
// the pipeline's retry decision depends on.
func TestOpenAICompatArm_RequestAndErrorTaxonomy(t *testing.T) {
	ctx := context.Background()

	var got openAICompatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// assert, not require: this runs on the server's goroutine, and
		// require's FailNow is only valid on the test goroutine.
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&got)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer k", r.Header.Get("Authorization"))
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.25, -0.5}}},
		}))
	}))
	defer server.Close()

	arm := &openAICompatEmbedder{APIKey: "k", BaseURL: server.URL, Model: "m", Dimension: 256, client: http.DefaultClient}
	vecs, err := arm.embedFloatBatch(ctx, []string{"package main"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	assert.Equal(t, []float32{0.25, -0.5}, vecs[0], "the decode must be float32, not the int narrowing the Voyage arm uses")
	assert.Equal(t, openAICompatWireEncodingFormat, got.EncodingFormat)
	assert.Equal(t, "m", got.Model)
	assert.Equal(t, 256, got.Dimensions)
	assert.Equal(t, []string{"package main"}, got.Input)

	// The taxonomy: 429 transient, 400 terminal, both carrying http_<code>.
	for _, tc := range []struct {
		status    int
		transient bool
	}{{http.StatusTooManyRequests, true}, {http.StatusInternalServerError, true}, {http.StatusBadRequest, false}} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		failing := &openAICompatEmbedder{APIKey: "k", BaseURL: bad.URL, Model: "m", client: http.DefaultClient}
		_, err := failing.embedFloatBatch(ctx, []string{"x"})
		require.Error(t, err)
		var llmErr *llm.LLMError
		require.ErrorAs(t, err, &llmErr, "every failure must be an *llm.LLMError, got %T", err)
		assert.Equal(t, tc.transient, llmErr.Transient, "status %d transient", tc.status)
		assert.True(t, strings.HasPrefix(llmErr.Reason, "http_"), "reason %q must carry http_<code>", llmErr.Reason)
		bad.Close()
	}

	// A dead endpoint is a network failure: transient, so the pipeline
	// retries next tick instead of writing a terminal marker.
	dead := &openAICompatEmbedder{APIKey: "k", BaseURL: "http://127.0.0.1:1", Model: "m", client: http.DefaultClient}
	_, err = dead.embedFloatBatch(ctx, []string{"x"})
	require.Error(t, err)
	var llmErr *llm.LLMError
	require.ErrorAs(t, err, &llmErr)
	assert.Equal(t, "network", llmErr.Reason)
	assert.True(t, llmErr.Transient)
}

// TestOpenAICompatArm_PacksAtItsOwnCap proves the arm packs against its
// OWN named constant rather than the Voyage arm's, and that the cap is a
// real bound rather than an unused declaration.
func TestOpenAICompatArm_PacksAtItsOwnCap(t *testing.T) {
	texts := make([]string, openaiCompatBatchSize+3)
	for i := range texts {
		texts[i] = "t"
	}
	packs := packOpenAICompat(texts)
	require.Len(t, packs, 2, "one full pack plus a remainder")
	assert.Len(t, packs[0], openaiCompatBatchSize)
	assert.Len(t, packs[1], 3)

	// Order and totality: every input appears exactly once, in order.
	total := 0
	for _, p := range packs {
		total += len(p)
	}
	assert.Equal(t, len(texts), total)

	// The arm's cap is its own number, not the Voyage arm's.
	assert.NotEqual(t, embedBatchSize, openaiCompatBatchSize, "one provider's cap must not govern another's")
	assert.Nil(t, packOpenAICompat(nil))
}
