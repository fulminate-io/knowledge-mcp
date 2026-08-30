// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestResolvedEmbedDtype_RefusesAMalformedSectionAndDefaultsAnAbsentOne pins the
// distinction the rebuild and repair paths depend on.
//
// THESE TWO CASES LOOK ALIKE AND ARE NOT. Both used to answer "" — which the
// vector format reads as ubinary — so a MALFORMED [embedder] section silently
// asserted that a corpus being re-sealed was ubinary. On a float32 corpus that
// re-seals IEEE bit patterns to be ranked by Hamming distance: bytes preserved,
// lengths quiet, ordering wrong, nothing reporting a problem. An ABSENT section
// is the opposite: ResolveEmbedder DEFINES it as the documented defaults, and
// float32 is reachable only by configuring it explicitly, so an absent config
// cannot have produced a float32 vector to mis-tag.
//
// BOTH LEGS ARE REQUIRED TO DISCRIMINATE. A helper that errored on everything
// satisfies the refusal leg alone and would break every deployment without an
// [embedder] section; one that never errors satisfies the default leg alone and
// reinstates the silent mis-tag.
func TestResolvedEmbedDtype_RefusesAMalformedSectionAndDefaultsAnAbsentOne(t *testing.T) {
	t.Run("an absent section resolves to the documented default", func(t *testing.T) {
		restore := config.SetForTest(&config.Config{})
		t.Cleanup(restore)

		got, err := resolvedEmbedDtype()
		require.NoError(t, err,
			"a config with no [embedder] section must RESOLVE — erroring here breaks rebuild and "+
				"repair for every deployment that never wrote one")
		require.Equal(t, searchengine.DtypeUbinary, got,
			"and it resolves to ubinary, which is what ResolveEmbedder defines an absent section as")
	})

	t.Run("a malformed section is refused", func(t *testing.T) {
		// Dimension 999 is outside AcceptedEmbedDimensions, so ResolveEmbedder
		// reports a fault rather than a section.
		restore := config.SetForTest(&config.Config{
			Embedder: &config.EmbedSection{Provider: config.EmbedProviderVoyage, Dimension: 999},
		})
		t.Cleanup(restore)

		got, err := resolvedEmbedDtype()
		require.Error(t, err,
			"a section that does not resolve must be REPORTED; answering \"\" would assert these "+
				"vectors are ubinary on a path that re-seals a corpus it did not produce")
		require.Empty(t, got, "and a refused resolution must not also hand back a usable dtype")
	})
}

// TestBuildRebuildDocs_RefusesWhenTheRepresentationIsUnknown proves the refusal
// reaches the PRODUCER, not just the helper — a caller that ignored the new
// error would reinstate the silent mis-tag one frame up, which is exactly the
// shape this whole class of defect keeps taking.
func TestBuildRebuildDocs_RefusesWhenTheRepresentationIsUnknown(t *testing.T) {
	chunk := []rebuildSegItem{{nodeID: "n1", vector: make([]byte, 32)}}

	// KNOWN-POSITIVE FIRST, same run: with a resolvable config the producer
	// builds and tags. Without this, a producer that had simply started failing
	// on everything would satisfy the refusal below.
	restore := config.SetForTest(&config.Config{})
	hnsw, _, err := buildRebuildDocs(chunk)
	restore()
	require.NoError(t, err)
	require.Len(t, hnsw, 1)
	require.Equal(t, searchengine.DtypeUbinary, hnsw[0].Dtype,
		"the control must actually TAG the document, or the refusal below proves nothing")

	restore = config.SetForTest(&config.Config{
		Embedder: &config.EmbedSection{Provider: config.EmbedProviderVoyage, Dimension: 999},
	})
	t.Cleanup(restore)

	hnsw, _, err = buildRebuildDocs(chunk)
	require.Error(t, err,
		"a rebuild that cannot determine the representation of the stored vectors it is re-sealing "+
			"must ABORT rather than tag them with a guess")
	require.Nil(t, hnsw, "and it must not hand back documents it could not tag")
}
