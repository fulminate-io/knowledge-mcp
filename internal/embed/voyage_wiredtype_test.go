// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// captureVoyageBody stands up a server that records the RAW request bytes of
// the first embeddings call and answers with one 1-byte vector, so an
// assertion can be made about the exact bytes on the wire rather than about a
// struct the test decoded them back into.
func captureVoyageBody(t *testing.T, e *voyageEmbedder) []byte {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1]}]}`))
	}))
	t.Cleanup(srv.Close)

	e.BaseURL = srv.URL
	_, err := e.EmbedBinaryBatch(context.Background(), []string{"package main"})
	require.NoError(t, err)
	return raw
}

// TestVoyageArm_Float32RequestCarriesWireSpelling pins the seam this file
// exists for: what the arm PUTS IN output_dtype is Voyage's vocabulary, never
// the config's.
//
// THE EXPECTED SPELLING IS OBSERVED, NOT INVENTED. It comes from the provider's
// own rejection of the config spelling, recorded verbatim in
// testdata/voyage_float32_wire_verification.txt: a 400 whose body enumerates
// the accepted set for this model as ['binary', 'float', 'int8', 'ubinary',
// 'uint8']. That enumeration is why "float" is written here as a literal rather
// than reasoned into existence.
func TestVoyageArm_Float32RequestCarriesWireSpelling(t *testing.T) {
	e := &voyageEmbedder{
		Model: "voyage-3", APIKey: "test",
		Dimension: 1024, Dtype: voyageDtypeFloat32, client: http.DefaultClient,
	}
	var got voyageEmbedRequest
	require.NoError(t, json.Unmarshal(captureVoyageBody(t, e), &got))

	assert.Equal(t, "float", got.OutputType,
		"a float32 profile must ask for the spelling Voyage accepts; the provider rejected %q outright",
		voyageDtypeFloat32)
	assert.NotEqual(t, voyageDtypeFloat32, got.OutputType,
		"the config dtype must not reach the wire — that conflation is the defect")

	// The rest of the request is untouched by the translation: the width still
	// reaches the provider, since the response width follows from it.
	assert.Equal(t, 1024, got.OutputDim)
	assert.Equal(t, "voyage-3", got.Model)
}

// TestVoyageArm_UbinaryRequestBodyUnchanged is the CONTROL for the corpus that
// exists today: every stored vector was produced by a ubinary request, and the
// change that makes float32 reachable is the one most likely to disturb it.
//
// IT ASSERTS THE WHOLE BODY, BYTE FOR BYTE, against a hand-written literal
// rather than against a struct rebuilt by the code under test — a re-marshal
// would agree with any translation the arm applied to itself. "ubinary" is also
// in the observed accepted set, so this body is known to be one the provider
// takes.
func TestVoyageArm_UbinaryRequestBodyUnchanged(t *testing.T) {
	e := &voyageEmbedder{
		Model: "voyage-3", APIKey: "test",
		Dimension: 256, Dtype: voyageDtypeUbinary, client: http.DefaultClient,
	}
	const want = `{"input":["package main"],"model":"voyage-3","input_type":"document","output_dimension":256,"output_dtype":"ubinary"}`

	// COMPARED AS BYTES BY HAND, not through a JSON-aware assertion. A
	// semantic JSON comparison would pass on a body with different field
	// order, different whitespace or a different encoding of the same values,
	// and "the bytes did not move" is exactly the claim being made here.
	if got := string(captureVoyageBody(t, e)); got != want {
		t.Fatalf("the ubinary request body must be byte-identical to what this arm has always sent\n want: %s\n  got: %s", want, got)
	}
}

// TestVoyageArm_WireAndConfigDtypeVocabulariesAreDistinct states as an
// ASSERTION what the constants' comments state as prose: the config dtype
// vocabulary and the output_dtype wire vocabulary are different sets that
// happen to coincide at one member. If a later edit collapses them back into
// one constant — the shape that put "float32" on the wire and drew a 400 —
// this fails.
//
// THE OBSERVED SPELLINGS ARE PINNED AS LITERALS, not read off the constants
// they check: a constant compared against itself would agree with any value a
// later edit gave it. The literals come from the provider's own enumeration of
// its accepted set, recorded in
// testdata/voyage_float32_wire_verification.txt.
func TestVoyageArm_WireAndConfigDtypeVocabulariesAreDistinct(t *testing.T) {
	require.NotEqual(t, voyageDtypeFloat32, voyageWireDtypeFloat,
		"the config dtype vocabulary and the output_dtype wire vocabulary are different sets")
	require.Equal(t, "float", voyageWireDtypeFloat,
		"the unquantized wire spelling is the one the provider's rejection enumerated")
	require.Equal(t, "ubinary", voyageWireDtypeUbinary,
		"the quantized wire spelling is the one the recorded verification already passed on")

	// WHERE THE TWO SETS COINCIDE THEY MUST STAY EQUAL. This is the leg that
	// keeps the separation from being applied blindly: translating ubinary to
	// anything else would break every corpus that exists today.
	require.Equal(t, voyageDtypeUbinary, voyageWireDtypeUbinary,
		"ubinary is spelled the same in both vocabularies, and the translation must not invent a difference")
}

// TestVoyageArm_UntranslatableDtypeIsRefusedBeforeAnyRequest covers the branch
// the translation added: a dtype with no observed wire spelling.
//
// IT IS REFUSED RATHER THAN FORWARDED, which is the whole point — forwarding an
// untranslated value is what sent "float32" and drew the 400. The refusal is
// TERMINAL (the same value fails identically forever) and it names both sides,
// so an operator is not left guessing the spelling that works.
//
// THE ZERO IS CONTROLLED IN THE SAME RUN. "no request was made" is asserted
// against a counter that a translatable dtype drives non-zero on the same
// server, so a counter that was never wired cannot pass for a refusal.
func TestVoyageArm_UntranslatableDtypeIsRefusedBeforeAnyRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1]}]}`))
	}))
	defer srv.Close()

	arm := func(dtype string) *voyageEmbedder {
		return &voyageEmbedder{
			BaseURL: srv.URL, Model: "voyage-3", APIKey: "test",
			Dimension: 256, Dtype: dtype, client: http.DefaultClient,
		}
	}

	// KNOWN POSITIVE, first: a translatable dtype does reach the transport, so
	// the zero below means "refused" rather than "this server is never called".
	_, err := arm(voyageDtypeUbinary).EmbedBinaryBatch(context.Background(), []string{"package main"})
	require.NoError(t, err)
	require.Equal(t, 1, requests, "a translatable dtype must reach the provider")

	// "int8" is a value VOYAGE accepts but this build's config vocabulary does
	// not carry, so it is exactly the shape that must not be forwarded on a
	// guess: the arm cannot decode it.
	for _, dtype := range []string{"int8", "binary", "float", ""} {
		_, err := arm(dtype).EmbedBinaryBatch(context.Background(), []string{"package main"})
		require.Error(t, err, "dtype %q has no observed translation and must be refused", dtype)
		require.ErrorIs(t, err, ErrInvalidConfig)
		assert.False(t, llm.IsTransient(err),
			"an untranslatable dtype fails identically on every retry, so it must be terminal")
		if dtype != "" {
			assert.Contains(t, err.Error(), dtype, "the refusal must name the value it could not translate")
		}
		assert.Contains(t, err.Error(), voyageWireDtypeFloat,
			"and must name a wire spelling it CAN produce, or the operator is left guessing")
		assert.Contains(t, err.Error(), voyageDtypeFloat32,
			"paired with the config dtype that reaches it, since that is what an operator writes")
	}

	require.Equal(t, 1, requests,
		"no refused dtype may reach the provider — the request must never be built")
}
