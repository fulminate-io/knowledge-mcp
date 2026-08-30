// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBuildRefusesMixedVectorWidth covers the width authority Build and Merge now
// take from their documents: a batch that mixes widths is REFUSED, and the
// separate empty-vector skip is not collateral damage of that refusal.
func TestBuildRefusesMixedVectorWidth(t *testing.T) {
	t.Parallel()

	t.Run("error names both widths and the id", func(t *testing.T) {
		t.Parallel()

		// KNOWN-POSITIVE FIRST. A uniform batch at a NON-DEFAULT width must build,
		// which is the whole point of taking the width from the documents. Without
		// this arm, an implementation that rejected every non-32-byte batch would
		// satisfy the refusal assertions below and still be broken.
		wide := []searchengine.Document{
			{ID: "a", Vector: make([]byte, 128)},
			{ID: "b", Vector: make([]byte, 128)},
			{ID: "c", Vector: make([]byte, 128)},
		}
		seg, err := Format{}.Build(wide)
		require.NoError(t, err, "a uniform batch at a non-default width must build")
		require.Len(t, seg.IDs(), 3)
		vec, ok := seg.(*hnswSegment).VectorByID("a")
		require.True(t, ok)
		require.Len(t, vec, 128,
			"the stored vector keeps the DOCUMENTS' width — a fixed 32 here was the silent corruption")

		// Now the refusal. "b" is the odd one out, at 64 bytes in a 128-byte batch.
		mixed := []searchengine.Document{
			{ID: "a", Vector: make([]byte, 128)},
			{ID: "b", Vector: make([]byte, 64)},
			{ID: "c", Vector: make([]byte, 128)},
		}
		_, err = Format{}.Build(mixed)
		require.Error(t, err, "a mixed-width batch must be REFUSED, never coerced to some width")
		require.ErrorIs(t, err, ErrMixedVectorWidth, "callers must be able to match the sentinel")

		msg := err.Error()
		require.Contains(t, msg, `"b"`, "the error names the OFFENDING ID")
		require.Contains(t, msg, "64", "the error names the width it found")
		require.Contains(t, msg, "128", "the error names the width the batch is")

		// Merge refuses on the same authority. Two segments built at different
		// widths cannot be consolidated into one graph, and the refusal must come
		// from the width check rather than from a panic deep in the distance
		// function.
		narrowSeg, err := Format{}.Build([]searchengine.Document{
			{ID: "n1", Vector: make([]byte, 32)},
			{ID: "n2", Vector: make([]byte, 32)},
		})
		require.NoError(t, err)
		_, err = mergeSegments(t,
			[]searchengine.Segment[[]byte, struct{}]{seg, narrowSeg},
			[]func(searchengine.ExternalID) bool{nil, nil},
		)
		require.Error(t, err, "merging segments of different widths must be refused")
		require.ErrorIs(t, err, ErrMixedVectorWidth,
			"the merge refusal is the same width authority, wrapped")
	})

	t.Run("empty vectors are still skipped", func(t *testing.T) {
		t.Parallel()

		// The empty-vector skip is the formats-tolerate-absent-data contract and is
		// a DIFFERENT rule from the width refusal. A width check that treated an
		// absent vector as "width 0" would turn this batch into a mixed-width
		// refusal and break that contract, so this asserts the two rules stay
		// separate.
		batch := []searchengine.Document{
			{ID: "a", Vector: make([]byte, 64)},
			{ID: "empty1", Vector: nil},
			{ID: "b", Vector: make([]byte, 64)},
			{ID: "empty2", Vector: []byte{}},
			{ID: "c", Vector: make([]byte, 64)},
		}
		seg, err := Format{}.Build(batch)
		require.NoError(t, err, "documents with absent vectors are skipped, not refused")

		ids := seg.IDs()
		require.ElementsMatch(t, []searchengine.ExternalID{"a", "b", "c"}, ids,
			"exactly the vector-bearing documents are indexed")
		require.NotContains(t, ids, searchengine.ExternalID("empty1"))
		require.NotContains(t, ids, searchengine.ExternalID("empty2"))

		// An ALL-empty batch still yields an empty, searchable, zero-hit segment —
		// the documented contract, which the width derivation must not turn into an
		// error for want of a width to report.
		allEmpty, err := Format{}.Build([]searchengine.Document{
			{ID: "x", Vector: nil},
			{ID: "y", Vector: []byte{}},
		})
		require.NoError(t, err, "an all-empty batch must still build")
		require.Empty(t, allEmpty.IDs())
		require.Empty(t, allEmpty.Search(make([]byte, defaultVecBytes), struct{}{}, 5, nil),
			"an empty segment is searchable and returns no hits")
	})
}
