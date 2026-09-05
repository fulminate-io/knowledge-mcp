// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestBranchSeedDoesNotResurrectRetiredDocs is the CATCHER for the one operand in
// the branch seed that must not be swapped mechanically.
//
// THE HAZARD, in the seed's own words: "An L2 directory can legitimately hold
// superseded blobs no manifest references, and copying one RESURRECTS documents the
// base already retired." The seed used to read base's published set through its
// segment source — a remote manifest read, inherently the live set. That source is
// deleted, and the obvious replacement is baseCache.Keys() or any of the
// load-then-Export helpers that import it. ALL of those are the directory listing,
// so all of them re-open this hazard.
//
// The fixture is the supersession case specifically: base's L2 holds a blob that is
// NOT in base's live layer. manager_seed_branch_test.go exercises the copy, but not
// this — no existing test covers it.
//
// TWO LEGS. Without the second, a seed that copied NOTHING at all would pass.
func TestBranchSeedDoesNotResurrectRetiredDocs(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const (
		base     = "seedBase"
		branch   = "seedBase@feature"
		retired  = searchengine.SegmentID("seg-retired-by-base")
		retiredB = `[{"id":"retired-doc","content":"gone"}]`
	)

	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))

	// Base builds a real corpus, so its engine holds a live layer.
	stageRebuildRun(t, ctx, mgr, kgtypes.GraphCode, base, vecContentDocs(1024))
	res, err := mgr.FinalizeRebuild(ctx, kgtypes.GraphCode, base)
	require.NoError(t, err)
	require.True(t, res.Swapped, "base's layer must land, or there is no live set to seed from")

	liveIDs := make(map[searchengine.SegmentID]struct{})
	for _, b := range mgr.managerFor(kgtypes.GraphCode, base).engine.Export() {
		liveIDs[b.ID] = struct{}{}
	}
	require.NotEmpty(t, liveIDs, "base must export a live set")

	// THE SUPERSEDED BLOB: on disk in base's bucket, absent from base's live layer.
	// This is the state a retired layer leaves behind between a swap and the reclaim
	// that unlinks it.
	plantBlob(t, cacheDir, base, format, retired, []byte(retiredB))
	require.NotContains(t, liveIDs, retired,
		"the planted blob must NOT be in base's live layer, or the fixture proves nothing")

	branchCache := branchEngineCache(cacheDir, branch, format, 0)
	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, base, branch, format, branchCache)
	require.NoError(t, err)

	// LEG 1 — the retired blob was NOT copied.
	branchIDs := branchBucketIDs(cacheDir, branch, format)
	require.NotContains(t, branchIDs, retired,
		"the branch seed copied a blob base had already retired — a search on the branch would return "+
			"documents the base removed")
	for _, m := range seeded {
		require.NotEqual(t, retired, m.ID, "the retired blob must not be reported as seeded either")
	}

	// LEG 2, THE KNOWN-POSITIVE — base's LIVE blobs WERE copied. Without this a seed
	// that copied nothing at all would satisfy leg 1.
	require.NotEmpty(t, branchIDs,
		"the seed must copy base's live partitions — an empty branch bucket passes leg 1 vacuously")
	for _, id := range branchIDs {
		require.Contains(t, liveIDs, id,
			"every blob in the branch bucket must come from base's LIVE layer; %s did not", id)
	}
}
