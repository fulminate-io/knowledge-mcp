// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manager_seed_visibility_test.go — the seed takes effect IN THE PROCESS THAT RAN
// IT, not only after a restart.
//
// THE DEFECT THIS PINS. A diskSegmentCache indexes its directory ONCE, at
// construction, and Keys() never re-reads it. While the seed copied into a
// private second instance over the same directory, the engine's own instance —
// built moments earlier, when the branch directory was still empty — kept an
// empty index, imported nothing, and latched. Every configurational signal said
// the branch was seeded (both formats logged success, the blobs were on disk with
// byte-identical hashes, the rebuild record was written) while the serving engine
// held nothing for the rest of the process.

// seedVisibilityDoc is the term only BASE ever writes. Searching for it makes
// "the branch serves base's content" and "the branch serves something" different
// observations.
const seedVisibilityDoc = "pkg/base.go:SeedVisible"
const seedVisibilityTerm = "quokkasignal"

// warmSeedVisibilityBase writes one document into base's buckets in BOTH formats
// and re-emits them, which is what puts real blobs in the L2 cache for the seed
// to copy. It goes through the ordinary write path rather than planting files, so
// the blobs are ones the engines can actually decode and search.
func warmSeedVisibilityBase(t *testing.T, ctx context.Context, mgr *Manager, repo string) {
	t.Helper()
	vec := make([]byte, 32)
	for i := range vec {
		vec[i] = 'b'
	}
	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, []searchengine.Document{
		{ID: seedVisibilityDoc, Vector: vec},
	}))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, repo, []searchengine.Document{
		{ID: seedVisibilityDoc, Fields: map[string]string{
			searchengine.FieldSymbolName: seedVisibilityDoc,
			searchengine.FieldContent:    seedVisibilityTerm,
		}},
	}))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
}

func seedVisibilityHitIDs(hits []searchengine.Hit) []searchengine.ExternalID {
	out := make([]searchengine.ExternalID, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}

// TestBranchSeed_SeededBucketIsVisibleToTheSeedingEngine drives the OSS rail end
// to end: a logged-OUT Manager selects the L2-only source, so the cache directory
// IS the corpus and a search is a real read of what the seed copied.
func TestBranchSeed_SeededBucketIsVisibleToTheSeedingEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const repo = "visible-repo"
	const branch = repo + "@feature"

	t.Run("same_process_search_hits", func(t *testing.T) {
		t.Parallel()
		cacheDir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		warmSeedVisibilityBase(t, ctx, mgr, repo)

		// Touching the branch constructs its engines, which is what runs the seed;
		// the search then reads whatever the seed made visible to THOSE engines.
		hits, err := mgr.Search(ctx, kgtypes.GraphCode, branch, seedVisibilityTerm, nil, 10)
		require.NoError(t, err)
		require.Contains(t, seedVisibilityHitIDs(hits), searchengine.ExternalID(seedVisibilityDoc),
			"the branch must serve base's document IN THIS PROCESS — a seed visible only after a restart "+
				"leaves the branch serving an empty corpus for the life of the daemon")

		// The hit is a MATCH, not a passthrough: a term base never wrote returns
		// nothing, so the hit above cannot be an engine handing back its whole corpus.
		absent, err := mgr.Search(ctx, kgtypes.GraphCode, branch, "wombatsignal", nil, 10)
		require.NoError(t, err)
		require.NotContains(t, seedVisibilityHitIDs(absent), searchengine.ExternalID(seedVisibilityDoc),
			"control: a term BASE never wrote must not hit, or the hit above says nothing about content")
	})

	t.Run("restart_control_hits", func(t *testing.T) {
		t.Parallel()
		cacheDir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		warmSeedVisibilityBase(t, ctx, mgr, repo)
		_, err := mgr.Search(ctx, kgtypes.GraphCode, branch, seedVisibilityTerm, nil, 10)
		require.NoError(t, err)

		// THE KNOWN-POSITIVE CONTROL, and what makes the first subtest's red
		// readable. A SECOND Manager over the SAME directory re-scans it at
		// construction, so it sees the blobs the seed already copied. This arm passed
		// even against the defect — the finding observed exactly it — so a run where
		// BOTH arms are red means the fixture never indexed anything or the blobs
		// never landed, which is a broken probe rather than the defect.
		restarted := closeOnCleanup(t, NewManager(cacheDir, 0))
		hits, err := restarted.Search(ctx, kgtypes.GraphCode, branch, seedVisibilityTerm, nil, 10)
		require.NoError(t, err)
		require.Contains(t, seedVisibilityHitIDs(hits), searchengine.ExternalID(seedVisibilityDoc),
			"a fresh Manager over the same cache directory must serve base's document — if this arm is also "+
				"red the fixture is broken, not the code under test")
	})

	t.Run("engine_cache_keys_include_seeded", func(t *testing.T) {
		t.Parallel()
		cacheDir := t.TempDir()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		warmSeedVisibilityBase(t, ctx, mgr, repo)
		_, err := mgr.Search(ctx, kgtypes.GraphCode, branch, seedVisibilityTerm, nil, 10)
		require.NoError(t, err)

		// THE DIRECT STATEMENT OF THE FIX. The search subtest could in principle be
		// satisfied by some other path warming the engine; this pins that the seed's
		// writes landed in the very instance the engine reads. Asking the memoized
		// manager returns the same distManager the search used — the constructors are
		// check-construct-store, so no second seed runs here.
		base := branchBucketIDs(cacheDir, repo, bm25.New().Name())
		require.NotEmpty(t, base, "fixture control: base's bucket must hold the blobs the seed copies from")

		engineKeys := mgr.bm25ManagerFor(kgtypes.GraphCode, branch).cache.Keys()
		require.Subset(t, engineKeys, base,
			"the ENGINE's own cache instance must enumerate the seeded ids — that instance is what load() "+
				"reads, so ids visible anywhere else are ids the engine never imports")
	})
}
