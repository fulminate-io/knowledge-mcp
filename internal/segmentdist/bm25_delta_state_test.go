// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// bm25CursorsFixture builds a two-layer cursor set — a base layer and an overlay —
// so the round trip below exercises the SET shape the wire actually carries rather
// than a single-element list that a scalar record could also have satisfied.
func bm25CursorsFixture() []*knowledgev1.LayerCursor {
	return []*knowledgev1.LayerCursor{
		{LayerKey: "default", AfterUpdatedAt: 4_000_000_000, AfterId: "t4"},
		{LayerKey: "default@sess-a", AfterUpdatedAt: 9_000_000_000, AfterId: "t9"},
	}
}

// TestBM25CursorRecord_RoundTripAndMissingIsZero covers the four properties the
// arm depends on, each of which is a different way the record could be wrong.
func TestBM25CursorRecord_RoundTripAndMissingIsZero(t *testing.T) {
	m := &Manager{cacheDir: t.TempDir()}
	const name = "default"

	t.Run("a_missing_record_is_zero_cursors_and_a_nil_error", func(t *testing.T) {
		// NOT an error, and not an empty-but-present record: the arm reads this as
		// "start from zero", which is an ordinary cold drain on this feed.
		got, err := m.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err, "an absent record is the first-run state, never a fault")
		assert.Empty(t, got)
	})

	t.Run("the_record_round_trips_per_graph", func(t *testing.T) {
		want := bm25CursorsFixture()
		require.NoError(t, m.SaveBM25Cursors(kgtypes.GraphKnowledge, name, want))

		got, err := m.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err)
		require.Len(t, got, 2, "both layers survive the round trip")
		// Field-by-field rather than proto equality: the assertion is about what the
		// RECORD preserved, and every one of these three is load-bearing — a dropped
		// AfterId silently re-serves a page, a dropped AfterUpdatedAt re-drains a
		// layer, and a dropped LayerKey misroutes both.
		for i, w := range want {
			assert.Equal(t, w.GetLayerKey(), got[i].GetLayerKey())
			assert.Equal(t, w.GetAfterUpdatedAt(), got[i].GetAfterUpdatedAt())
			assert.Equal(t, w.GetAfterId(), got[i].GetAfterId())
		}
	})

	t.Run("two_graphs_do_not_share_one_record", func(t *testing.T) {
		// THE KNOWN POSITIVE for the path derivation. Without it, a path helper that
		// ignored (gt, name) entirely would pass every other arm here.
		require.NoError(t, m.SaveBM25Cursors(kgtypes.GraphCode, "repo-a",
			[]*knowledgev1.LayerCursor{{LayerKey: "repo-a", AfterUpdatedAt: 1, AfterId: "a"}}))

		knowledge, err := m.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err)
		require.Len(t, knowledge, 2, "the knowledge graph still holds its own two-layer set")
		assert.Equal(t, "default", knowledge[0].GetLayerKey())

		code, err := m.LoadBM25Cursors(kgtypes.GraphCode, "repo-a")
		require.NoError(t, err)
		require.Len(t, code, 1)
		assert.Equal(t, "repo-a", code[0].GetLayerKey())

		other, err := m.LoadBM25Cursors(kgtypes.GraphCode, "repo-b")
		require.NoError(t, err)
		assert.Empty(t, other, "a graph that never wrote a record must not read a sibling's")
	})

	t.Run("reset_returns_a_written_record_to_the_missing_state", func(t *testing.T) {
		// Precondition asserted rather than assumed: resetting something that was
		// never there would prove nothing.
		before, err := m.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err)
		require.NotEmpty(t, before, "the record must be present before the reset")

		require.NoError(t, m.ResetBM25Cursors(kgtypes.GraphKnowledge, name))

		after, err := m.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err, "a reset record reads exactly like one that never existed")
		assert.Empty(t, after)

		// The file is GONE, not emptied — the distinction the reset doc turns on.
		_, statErr := filepath.Glob(bm25DeltaStatePathFor(m.cacheDir, kgtypes.GraphKnowledge, name))
		require.NoError(t, statErr)
		assert.NoFileExists(t, bm25DeltaStatePathFor(m.cacheDir, kgtypes.GraphKnowledge, name))

		// And a SECOND reset is success, not a not-exist error: an absent record is
		// the intended end state, so the operation is idempotent.
		require.NoError(t, m.ResetBM25Cursors(kgtypes.GraphKnowledge, name),
			"resetting an already-absent record has done its job")

		// The sibling graph is untouched by the reset.
		code, err := m.LoadBM25Cursors(kgtypes.GraphCode, "repo-a")
		require.NoError(t, err)
		assert.Len(t, code, 1, "a reset is scoped to its own graph")
	})

	t.Run("a_manager_with_no_cache_dir_keeps_no_record", func(t *testing.T) {
		none := &Manager{}
		require.NoError(t, none.SaveBM25Cursors(kgtypes.GraphKnowledge, name, bm25CursorsFixture()))
		got, err := none.LoadBM25Cursors(kgtypes.GraphKnowledge, name)
		require.NoError(t, err)
		assert.Empty(t, got)
		require.NoError(t, none.ResetBM25Cursors(kgtypes.GraphKnowledge, name))
	})
}
