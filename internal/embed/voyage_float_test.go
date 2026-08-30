// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// cannedVoyageServer serves one fixed embeddings payload, verbatim.
//
// A CANNED SERVER, NEVER A LIVE PROVIDER CALL. The assertion here is about THIS
// repo's decoding of a response shape; a live call would need a key, cost money
// per run, and fail for reasons that are not this code's. What the canned body
// cannot establish — that Voyage's real float response is shaped this way — is a
// claim about a third-party API, and it is STILL UNOBSERVED: the one live call
// this repo has spent on the float path was refused on argument validation and
// never reached embedding generation, so it settled the request-side spelling
// and nothing about the response. That record, including what it does not
// establish, is testdata/voyage_float32_wire_verification.txt.
func cannedVoyageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestVoyageFloatBatch drives canned payloads through the arm's REAL decode
// path — EmbedBinaryBatch, not decodeVoyageEmbedding directly — so the dtype
// the request carries is the one the decode consults, rather than one the test
// hands in.
func TestVoyageFloatBatch(t *testing.T) {
	t.Run("float response decodes to little endian bytes", func(t *testing.T) {
		// 1.0, -2.0 and 0.5 chosen because their IEEE-754 bit patterns are
		// asymmetric across the four bytes, so a big-endian encoder produces
		// DIFFERENT bytes and is caught. A value like 0.0 would pass under either
		// order and prove nothing about byte order at all.
		srv := cannedVoyageServer(t, `{"data":[{"embedding":[1.0,-2.0,0.5]}]}`)

		e := &voyageEmbedder{
			BaseURL: srv.URL, Model: "voyage-3", APIKey: "test",
			Dimension: 3, Dtype: voyageDtypeFloat32, client: http.DefaultClient,
		}
		got, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.NoError(t, err)
		require.Len(t, got, 1)

		// THE EXPECTATION IS HAND-WRITTEN, not produced by the encoder under
		// test. Round-tripping through binary.LittleEndian here would agree with
		// itself under any byte order and would pass on a platform where the
		// segment reader disagrees — which is the exact failure this leg exists
		// to catch, since a byte-order mismatch yields finite wrong distances
		// rather than an error.
		//   1.0  = 0x3F800000 -> 00 00 80 3F little-endian
		//  -2.0  = 0xC0000000 -> 00 00 00 C0
		//   0.5  = 0x3F000000 -> 00 00 00 3F
		want := []byte{
			0x00, 0x00, 0x80, 0x3F,
			0x00, 0x00, 0x00, 0xC0,
			0x00, 0x00, 0x00, 0x3F,
		}
		require.Equal(t, want, got[0], "float32 values must encode little-endian, four bytes each")

		// FOUR BYTES PER DIMENSION, stated as a literal rather than recomputed.
		require.Len(t, got[0], 12, "3 float32 dimensions weigh 12 bytes")
	})

	t.Run("ubinary response still decodes", func(t *testing.T) {
		// THE SAME-RUN KNOWN-POSITIVE. Without it, an arm that had simply broken
		// decoding for every dtype would satisfy the refusal leg below and leave
		// the float leg's failure looking like the new path's problem.
		srv := cannedVoyageServer(t, `{"data":[{"embedding":[0,127,255,1]}]}`)

		e := &voyageEmbedder{
			BaseURL: srv.URL, Model: "voyage-3", APIKey: "test",
			Dimension: 32, Dtype: voyageDtypeUbinary, client: http.DefaultClient,
		}
		got, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, []byte{0x00, 0x7F, 0xFF, 0x01}, got[0],
			"ubinary values are one byte each, unchanged by the float path")

		// AND THE TWO PATHS DISAGREE, which is what proves the dtype is read. The
		// same four numbers under a float32 request weigh four times as much.
		fsrv := cannedVoyageServer(t, `{"data":[{"embedding":[0,127,255,1]}]}`)
		fe := &voyageEmbedder{
			BaseURL: fsrv.URL, Model: "voyage-3", APIKey: "test",
			Dimension: 4, Dtype: voyageDtypeFloat32, client: http.DefaultClient,
		}
		fgot, ferr := fe.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.NoError(t, ferr)
		require.Len(t, fgot[0], 16,
			"the same payload under float32 must weigh 4x — otherwise the dtype is being ignored")
	})

	t.Run("float response under a ubinary config is refused", func(t *testing.T) {
		srv := cannedVoyageServer(t, `{"data":[{"embedding":[0.25,-0.5,0.125]}]}`)

		e := &voyageEmbedder{
			BaseURL: srv.URL, Model: "voyage-3", APIKey: "test",
			Dimension: 24, Dtype: voyageDtypeUbinary, client: http.DefaultClient,
		}
		_, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.Error(t, err,
			"a float answer to a ubinary request must be REFUSED — quantizing it here would "+
				"invent a representation neither side asked for")
		require.Contains(t, err.Error(), voyageDtypeUbinary,
			"the refusal names the representation that was requested")
		require.Contains(t, err.Error(), "integer",
			"and states what it expected the values to be")

		// TERMINAL, NOT TRANSIENT. The same payload fails identically forever, so
		// a retryable classification would loop an embed worker on it.
		var llmErr *llm.LLMError
		require.ErrorAs(t, err, &llmErr, "the refusal must be a typed LLMError the worker can classify")
		require.False(t, llmErr.Transient, "a representation mismatch can never succeed on retry")
	})
}

// TestVoyageEmbedRequestCarriesConfiguredDtype pins the OTHER half of the seam:
// the arm asks for the REPRESENTATION it will then decode by, in the wire
// vocabulary rather than the config one.
//
// WITHOUT THIS THE DECODE TEST IS HALF A TEST. Every leg above hands the arm a
// dtype and checks how it reads the answer; none of them observes what the arm
// SENT. An arm that requested ubinary while decoding as float32 would satisfy
// all three and produce garbage against a real provider, because the canned
// server ignores the request body.
//
// IT ASSERTS THE TRANSLATED SPELLING, and that is not a weakening: the config
// spelling "float32" is a value the provider REFUSES (the verbatim 400 lives in
// testdata/voyage_float32_wire_verification.txt), so a request carrying it
// reaches no embedding at all. What must not drift is the representation, and
// voyageWireDtype is the one place the two spellings for it are tied together.
func TestVoyageEmbedRequestCarriesConfiguredDtype(t *testing.T) {
	var seen voyageEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1.0]}]}`))
	}))
	defer srv.Close()

	e := &voyageEmbedder{
		BaseURL: srv.URL, Model: "voyage-3", APIKey: "test",
		Dimension: 1024, Dtype: voyageDtypeFloat32, client: http.DefaultClient,
	}
	_, err := e.EmbedBinaryBatch(context.Background(), []string{"hello"})
	require.NoError(t, err)
	require.Equal(t, voyageWireDtypeFloat, seen.OutputType,
		"the arm must ask for the representation it decodes by, in Voyage's spelling, "+
			"or request and response disagree in production")
	require.NotEqual(t, voyageDtypeFloat32, seen.OutputType,
		"and it must not send the config spelling, which the provider refuses")
	require.Equal(t, 1024, seen.OutputDim,
		"and the configured dimension must reach the provider, since the response width follows from it")
}
