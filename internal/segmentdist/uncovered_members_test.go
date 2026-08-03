// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestUncoveredMembersLoadsAndReportsPerFormat pins the two properties the repair
// arm's diff depends on: the seam LOADS before answering, and it reports the two
// formats INDEPENDENTLY.
//
// Per-format independence is not a nicety — a document really can be resident in
// one format and absent from the other, and a caller given only the union would
// either miss a repair or re-ship a format that was already fine. The
// missing_from_bm25_only arm uses a DIFFERENT concrete id from its HNSW sibling so
// one value cannot satisfy both.
func TestUncoveredMembersLoadsAndReportsPerFormat(t *testing.T) {
	t.Parallel()

	const (
		gt   = kgtypes.GraphCode
		name = "uncovered"
	)
	// A doc needs a VECTOR to become resident in the HNSW format — a content-only
	// document publishes an empty live set there, while BM25 seals it from Fields
	// alone. Both are supplied so one fixture serves both formats.
	mkDoc := func(id string) searchengine.Document {
		vec := make([]byte, 32)
		for i := range vec {
			vec[i] = byte((len(id)*31 + i*7) % 251)
		}
		return searchengine.Document{
			ID:     id,
			Vector: vec,
			Fields: map[string]string{searchengine.FieldContent: "alpha " + id},
		}
	}
	// newMgr returns a manager whose HNSW set holds hnswIDs and whose BM25 set
	// holds bm25IDs, so the two formats can be made to disagree deliberately.
	newMgr := func(t *testing.T, hnswIDs, bm25IDs []string) *Manager {
		t.Helper()
		ctx := context.Background()
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		hdocs := make([]searchengine.Document, 0, len(hnswIDs))
		for _, id := range hnswIDs {
			hdocs = append(hdocs, mkDoc(id))
		}
		bdocs := make([]searchengine.Document, 0, len(bm25IDs))
		for _, id := range bm25IDs {
			bdocs = append(bdocs, mkDoc(id))
		}
		if len(hdocs) > 0 {
			require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, hdocs))
		}
		if len(bdocs) > 0 {
			require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, bdocs))
		}
		return mgr
	}

	t.Run("missing_from_hnsw_only", func(t *testing.T) {
		mgr := newMgr(t, []string{"shared"}, []string{"shared", "only-bm25-has-me"})
		h, b, err := mgr.UncoveredMembers(context.Background(), gt, name,
			[]searchengine.ExternalID{"shared", "only-bm25-has-me"})
		require.NoError(t, err)
		require.Equal(t, []searchengine.ExternalID{"only-bm25-has-me"}, h)
		require.Empty(t, b)
	})

	t.Run("missing_from_bm25_only", func(t *testing.T) {
		// A DIFFERENT concrete id from the arm above, so a seam that answered one
		// format for both cannot satisfy the pair.
		mgr := newMgr(t, []string{"shared", "only-hnsw-has-me"}, []string{"shared"})
		h, b, err := mgr.UncoveredMembers(context.Background(), gt, name,
			[]searchengine.ExternalID{"shared", "only-hnsw-has-me"})
		require.NoError(t, err)
		require.Empty(t, h)
		require.Equal(t, []searchengine.ExternalID{"only-hnsw-has-me"}, b)
	})

	t.Run("missing_from_both", func(t *testing.T) {
		mgr := newMgr(t, []string{"present"}, []string{"present"})
		h, b, err := mgr.UncoveredMembers(context.Background(), gt, name,
			[]searchengine.ExternalID{"absent"})
		require.NoError(t, err)
		require.Equal(t, []searchengine.ExternalID{"absent"}, h)
		require.Equal(t, []searchengine.ExternalID{"absent"}, b)
	})

	t.Run("none_missing", func(t *testing.T) {
		// The converged case: a repair pass over this graph must ship nothing.
		mgr := newMgr(t, []string{"a", "b"}, []string{"a", "b"})
		h, b, err := mgr.UncoveredMembers(context.Background(), gt, name,
			[]searchengine.ExternalID{"a", "b"})
		require.NoError(t, err)
		require.Empty(t, h)
		require.Empty(t, b)
	})

	t.Run("unloaded_engine_loads_before_answering", func(t *testing.T) {
		// The catcher for a seam that answers from a cold engine: without the load
		// it would report the whole corpus missing and turn a no-op pass into a
		// corpus-scale re-ship. The fixture writes through one manager, then asks a
		// SECOND manager over the same store — whose engines have never been warmed.
		ctx := context.Background()
		_, gc := newSegmentHarness(t)
		dir := t.TempDir()
		writer := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc))
		docs := []searchengine.Document{mkDoc("persisted-a"), mkDoc("persisted-b")}
		require.NoError(t, writer.ReplaceBucket(ctx, gt, name, nil, docs))
		require.NoError(t, writer.ReplaceBucketFields(ctx, gt, name, nil, docs))

		cold := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc))
		require.Zero(t, cold.LiveResidentDocCount(gt, name),
			"fixture precondition: the second manager's engine must start cold, or this arm proves nothing")

		h, b, err := cold.UncoveredMembers(ctx, gt, name,
			[]searchengine.ExternalID{"persisted-a", "persisted-b"})
		require.NoError(t, err)
		require.Empty(t, h, "a cold engine must LOAD before answering, not report the corpus missing")
		require.Empty(t, b, "a cold engine must LOAD before answering, not report the corpus missing")
	})
}
