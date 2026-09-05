// SPDX-License-Identifier: Apache-2.0

package embed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestFloat32ProfileEndToEnd drives the WHOLE chain this phase built, in one
// test: a TOML [embedder.profile] at dimension 1024 dtype float32, the profile
// resolved from it, the voyage arm constructed from that profile, a canned float
// response decoded through the arm's real HTTP path, the emitted bytes measured,
// and those exact bytes sealed into a segment whose dtype is read back and whose
// scorer produces an ordering.
//
// IT EXISTS BECAUSE COVERING A SURFACE IS NOT COVERING THE PATH THROUGH IT. Each
// of this phase's other tests drives ONE hop against a fixture it constructs
// itself, so all three can be green while the values handed BETWEEN them are
// wrong — a dimension that never reaches the arm, bytes the format reads as a
// different representation. Every value below is carried from the previous hop
// rather than restated.
//
// WHAT IT CANNOT COVER, stated so the coverage claim is not overstated: it makes
// no live provider call, so it does not establish that Voyage's real float
// response is shaped the way the canned body says. That is a claim about a
// third-party API and is recorded as an open obligation in
// testdata/voyage_float32_wire_verification.txt rather than asserted here — it
// belongs in a one-time verification, not in a test that runs on every commit.
func TestFloat32ProfileEndToEnd(t *testing.T) {
	const (
		floatProfileTOML = `
[embedder]
provider = "voyage"

[embedder.profile.wide]
provider = "voyage"
dimension = 1024
dtype = "float32"
`
		ubinaryProfileTOML = `
[embedder]
provider = "voyage"

[embedder.profile.narrow]
provider = "voyage"
dimension = 256
dtype = "ubinary"
`
	)

	// cannedArm resolves the named profile out of the given TOML, builds the real
	// registered arm from it, and points it at a server serving body. Everything
	// the arm knows about width and representation therefore comes from the
	// config, never from the test.
	cannedArm := func(t *testing.T, toml, profile, body string) (embed.BinaryEmbedder, config.EmbedProfile) {
		t.Helper()
		cfg, err := config.Parse([]byte(toml))
		require.NoError(t, err, "the profile TOML must parse — a build that refused this shape would fail here")

		prof, err := cfg.EmbedProfileByName(profile)
		require.NoError(t, err, "the profile must resolve")

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		arm, err := embed.NewEmbedder(context.Background(), &embed.Config{
			Provider:  embed.Provider(prof.Provider.String()),
			APIKey:    "test",
			BaseURL:   srv.URL,
			Model:     "voyage-3",
			Dimension: prof.Dimension,
			Dtype:     prof.Dtype,
			InputRole: embed.InputRoleDocument,
		})
		require.NoError(t, err, "the arm must be constructible from the resolved profile")
		return arm, prof
	}

	// The canned float payload: four values whose dot products are easy to state
	// as literals below. Only four, deliberately — the response width is what the
	// provider returns, and the assertion is that FOUR BYTES PER RETURNED VALUE
	// come back, which is the invariant that holds at any dimension.
	const floatBody = `{"data":[{"embedding":[1.0,0.0,0.0,0.0]}]}`

	var floatVec []byte
	var floatProfile config.EmbedProfile

	t.Run("profile produces float32 bytes at four per dimension", func(t *testing.T) {
		arm, prof := cannedArm(t, floatProfileTOML, "wide", floatBody)
		floatProfile = prof

		require.Equal(t, 1024, prof.Dimension, "the profile must carry the width the TOML declared")
		require.Equal(t, searchengine.DtypeFloat32, prof.Dtype,
			"and the representation, which is the thing this whole phase exists to make producible")

		vecs, err := arm.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.NoError(t, err, "a float32 profile must EMBED rather than fail to decode its own response")
		require.Len(t, vecs, 1)
		floatVec = vecs[0]

		// FOUR BYTES PER VALUE, stated as a literal. The canned response carries
		// four values, so the emitted vector must be 16 bytes; at one byte per
		// value — the pre-change behaviour — it would be 4.
		require.Len(t, floatVec, 16,
			"four float32 values weigh four bytes each; a 4-byte result means the arm still decoded as ubinary")
	})

	t.Run("segment seals and scores them", func(t *testing.T) {
		require.NotEmpty(t, floatVec, "the previous leg must have produced the bytes this one seals")

		// THE BYTES ARE THE ARM'S OWN OUTPUT, carried across the seam rather than
		// rebuilt here. A fixture constructed in this leg would prove the format
		// works on bytes shaped the way this test imagines, which is the exact
		// thing an end-to-end test exists not to do.
		docs := []searchengine.Document{
			{ID: "a", Vector: floatVec, Dtype: floatProfile.Dtype},
			{ID: "b", Vector: orthogonal(floatVec), Dtype: floatProfile.Dtype},
		}
		seg, _, err := hnsw.New().Build(docs)
		require.NoError(t, err, "the arm's bytes must seal — a width or dtype disagreement fails here")

		hits := seg.Search(floatVec, struct{}{}, 2, nil)
		require.NotEmpty(t, hits, "the sealed segment must be searchable with the same bytes that built it")
		require.Equal(t, "a", hits[0].ID, "the vector queried against itself ranks first among orthogonal peers")

		// THE SCORE IS THE DOT PRODUCT, which is what proves the segment was
		// sealed as float32 rather than merely holding float bytes. The canned
		// vector is [1,0,0,0], so dot with itself is exactly 1.0 — and a ubinary
		// segment scoring these same 16 bytes by Hamming similarity would report
		// 1.0 only if every bit matched, which it does, so the DISCRIMINATING
		// assertion is b's score: orthogonal under dot is 0.0, while Hamming
		// similarity over two vectors differing in a handful of bits is near 1.
		require.InDelta(t, 1.0, hits[0].Score, 1e-6, "dot of the unit vector with itself is 1.0")
		require.Len(t, hits, 2)
		require.InDelta(t, 0.0, hits[1].Score, 1e-6,
			"an ORTHOGONAL vector scores 0.0 under the dot metric; under Hamming similarity these two "+
				"vectors differ in only a few bits and would score near 1.0, so this number is only "+
				"reachable if the segment was sealed and scored as float32")
	})

	t.Run("a ubinary profile is unchanged", func(t *testing.T) {
		// THE REGRESSION CONTROL for every corpus that exists today. The change
		// most likely to break them is the one that makes float32 work.
		arm, prof := cannedArm(t, ubinaryProfileTOML, "narrow",
			`{"data":[{"embedding":[0,127,255,1]}]}`)

		require.Equal(t, 256, prof.Dimension)
		require.Equal(t, searchengine.DtypeUbinary, prof.Dtype)

		vecs, err := arm.EmbedBinaryBatch(context.Background(), []string{"hello"})
		require.NoError(t, err)
		require.Len(t, vecs, 1)
		require.Equal(t, []byte{0x00, 0x7F, 0xFF, 0x01}, vecs[0],
			"a ubinary profile still yields ONE BYTE per returned value, byte-for-byte as before")

		docs := []searchengine.Document{{ID: "u", Vector: vecs[0], Dtype: prof.Dtype}}
		seg, _, err := hnsw.New().Build(docs)
		require.NoError(t, err)
		hits := seg.Search(vecs[0], struct{}{}, 1, nil)
		require.Len(t, hits, 1)
		require.Equal(t, "u", hits[0].ID)
		require.InDelta(t, 1.0, hits[0].Score, 1e-9,
			"a ubinary self-query is a Hamming similarity of 1.0 — the scoring every shipped corpus gets")
	})
}

// orthogonal returns a vector of the same width whose float32 values are zero
// everywhere the input is non-zero and one where it is zero, so its dot product
// with the input is exactly 0 while its BYTES differ from the input in only a
// few positions. That combination is what makes the score assertions above
// discriminate between the dot metric and the Hamming one.
func orthogonal(vec []byte) []byte {
	out := make([]byte, len(vec))
	// The canned vector is [1,0,0,0]; its orthogonal partner here is [0,1,0,0].
	copy(out[4:8], vec[0:4])
	return out
}
