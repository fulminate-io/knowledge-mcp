// SPDX-License-Identifier: Apache-2.0

package segmentdist

// put_failloud_test.go covers what the STRUCTURAL gates on the Put signature change
// cannot: that a failed write's error actually REACHES a caller who acts on it.
//
// THE STRUCTURAL LEGS PROVE A SHAPE, NOT A BEHAVIOUR. A grep for `_ = cache.Put(` and
// an ast walk for a catch-and-continue can both read zero against code that returns
// the error and then does nothing useful with it. These tests drive the three
// surviving write paths — the merge reclaim, the steady-state embed write, and the
// branch seed copy — with a write that genuinely fails, and assert on the OUTCOME.
//
// EVERY LEG RUNS BOTH DIRECTIONS. A test that only proves "a failing Put produces an
// error" is satisfiable by an implementation that always errors, which would break
// every write in the product while passing this file.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestReclaimAbortsWhenTheMergedBlobCannotBePersisted is the catcher for a swallowed
// write on the ordering that makes a swallow catastrophic.
//
// WHY THIS SITE. reclaimMerged Puts the consolidated blob BEFORE Removing the
// constituents it replaces. Post-merge that consolidated blob is the ONLY durable copy
// of those constituents' documents, so a Put whose failure is discarded removes the
// constituents after writing nothing — the whole corpus segment gone, with no error
// anywhere and no test noticing, because no other test makes the disk write fail.
func TestReclaimAbortsWhenTheMergedBlobCannotBePersisted(t *testing.T) {
	t.Parallel()

	constituents, merged := realMergeBlobs(t)
	res := searchengine.MergeResult{
		Merged:  merged,
		Removed: []searchengine.SegmentID{constituents[0].ID, constituents[1].ID},
	}

	// seedInto plants the pre-merge constituents so there is something a botched
	// reclaim could actually destroy. Without them the "nothing was removed"
	// assertion would hold trivially.
	seedInto := func(t *testing.T, real *diskSegmentCache) {
		t.Helper()
		for _, b := range constituents {
			require.NoError(t, real.Put(b.ID, b.Bytes),
				"fixture: seeding a constituent must succeed, or the test proves nothing")
		}
	}

	t.Run("a_failed_put_removes_NOTHING", func(t *testing.T) {
		t.Parallel()
		real := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
		seedInto(t, real)
		ic := newInstrumentedCache(real)
		ic.failPut = true

		newReclaimDMOverCache(t, ic).reclaimMerged(res)

		// THE DISCRIMINATING ASSERTION. A swallowed Put error would let the reclaim
		// walk on to step (c) and delete both constituents.
		require.Empty(t, ic.removedSet(),
			"a failed Put must ABORT the reclaim before a single constituent is removed — "+
				"the merged blob was never written, so those constituents are the only copy left")

		// And the constituents are genuinely still readable, not merely un-Removed.
		for _, b := range constituents {
			_, ok := real.Get(b.ID)
			require.True(t, ok, "constituent %s must still be on disk after an aborted reclaim", b.ID)
		}
	})

	t.Run("a_successful_put_DOES_reclaim", func(t *testing.T) {
		t.Parallel()
		// THE OTHER DIRECTION. Without this, an implementation that never reclaims
		// anything — or a fixture whose reclaim never ran at all — passes the leg
		// above and this file would be measuring nothing.
		real := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
		seedInto(t, real)
		ic := newInstrumentedCache(real)

		newReclaimDMOverCache(t, ic).reclaimMerged(res)

		removed := ic.removedSet()
		require.Len(t, removed, len(res.Removed),
			"CONTROL: a clean Put must let the reclaim remove every constituent")
		for _, id := range res.Removed {
			require.Contains(t, removed, id)
		}
		_, ok := real.Get(merged.ID)
		require.True(t, ok, "CONTROL: the merged blob must be readable after a clean reclaim")
	})
}

// TestEmbedWriteAbortsWhenTheBlobCannotBePersisted covers the STEADY-STATE writer.
//
// It is not reachable from the reclaim test above and had no behavioral gate anywhere
// in the plan: the structural legs prove no swallow SHAPE exists, not that this caller
// acts on the error it now receives.
func TestEmbedWriteAbortsWhenTheBlobCannotBePersisted(t *testing.T) {
	t.Parallel()

	t.Run("embed_write_returns_the_error", func(t *testing.T) {
		t.Parallel()
		// writeNewBlobsToL2 is, after the rail deletion, the primary steady-state
		// writer of the only surviving segment store.
		_, merged := realMergeBlobs(t)
		blobs := []searchengine.SegmentBlob{merged}

		real := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
		ic := newInstrumentedCache(real)
		ic.failPut = true

		err := newReclaimDMOverCache(t, ic).writeNewBlobsToL2(blobs)
		// Asserted on the RETURNED error, never on a log line: a WARN in the daemon
		// log is precisely the outcome this criterion exists to reject.
		require.ErrorIs(t, err, errInjectedPutFailure,
			"a failed embed write must REACH THE CALLER, not be logged and walked past")
		_, ok := real.Get(merged.ID)
		require.False(t, ok, "and nothing may be reported as persisted that was not written")
	})

	t.Run("embed_write_succeeds_when_the_disk_does", func(t *testing.T) {
		t.Parallel()
		// THE OTHER DIRECTION: an implementation returning an error unconditionally
		// would break every embed in the product and still pass the leg above.
		_, merged := realMergeBlobs(t)
		real := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
		ic := newInstrumentedCache(real)

		require.NoError(t, newReclaimDMOverCache(t, ic).writeNewBlobsToL2(
			[]searchengine.SegmentBlob{merged}))
		_, ok := real.Get(merged.ID)
		require.True(t, ok, "CONTROL: a clean embed write must actually persist the blob")
	})
}

// TestBranchSeedAbortsWhenACopiedBlobCannotBePersisted covers the BRANCH SEED copy —
// the other surviving non-reclaim writer, and the one whose failure is quietest.
//
// A copy that silently dropped a blob produces a branch seeded with a HOLE, and the
// branch's own searches would be the only thing that ever noticed.
func TestBranchSeedAbortsWhenACopiedBlobCannotBePersisted(t *testing.T) {
	t.Parallel()

	t.Run("branch_seed_aborts_rather_than_reporting_a_partial_copy", func(t *testing.T) {
		t.Parallel()
		// THE HOLE THIS CATCHES: SeedBranchBucketFromBase copies base's published
		// partitions into a branch bucket. A copy that silently dropped a blob would
		// produce a branch seeded with a HOLE, and the branch's own searches would be
		// the only thing that ever noticed.
		ctx := context.Background()
		cacheDir := t.TempDir()
		format := hnsw.New().Name()
		const repo = "failseed-repo"
		const branch = "failseed-repo@feature"

		// A REAL base live layer: the seed reads base's ENGINE export, so a fixture of
		// planted files with no engine behind them would make the seed copy nothing and
		// both legs would pass — the failing one for the wrong reason, the control one
		// not at all.
		seedBaseCorpus(t, ctx, cacheDir, repo, format, 2048) // 2048 docs -> 2 partitions
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		live := warmBaseLiveLayer(t, ctx, mgr, repo, format)
		require.Len(t, live, 2, "fixture control: base's live layer must be exactly two partitions")

		// FAILURE INJECTED AT THE REAL SEAM, not through a double. copyPartitions
		// takes a CONCRETE *diskSegmentCache, so there is no interface to substitute;
		// instead the branch bucket's root is made a REGULAR FILE, which is a genuine
		// unwritable destination — Put's own os.MkdirAll fails against it and returns
		// the wrapped error the production path would see from a full or read-only
		// disk. Nothing about the code under test is stubbed.
		branchRoot := graphCacheDirFor(cacheDir, kgtypes.GraphCode, branch, format)
		require.NoError(t, os.MkdirAll(filepath.Dir(branchRoot), 0o750))
		require.NoError(t, os.WriteFile(branchRoot, []byte("not a directory"), 0o600))

		seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
			branchEngineCache(cacheDir, branch, format, 0))
		require.Error(t, err,
			"a seed whose destination cannot be written must ABORT, not report a partial copy as success")
		require.Empty(t, seeded,
			"and it must not claim partitions were seeded when the copy failed")
	})

	t.Run("branch_seed_completes_when_the_destination_is_writable", func(t *testing.T) {
		t.Parallel()
		// THE OTHER DIRECTION for the seed leg. Without it, a seed hard-wired to
		// error — or one refusing for an unrelated reason such as a non-empty bucket
		// or a missing base record — would pass the leg above while copying nothing
		// in production.
		ctx := context.Background()
		cacheDir := t.TempDir()
		format := hnsw.New().Name()
		const repo = "okseed-repo"
		const branch = "okseed-repo@feature"

		// A REAL base live layer: the seed reads base's ENGINE export, so a fixture of
		// planted files with no engine behind them would make the seed copy nothing and
		// both legs would pass — the failing one for the wrong reason, the control one
		// not at all.
		seedBaseCorpus(t, ctx, cacheDir, repo, format, 2048) // 2048 docs -> 2 partitions
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
		live := warmBaseLiveLayer(t, ctx, mgr, repo, format)
		require.Len(t, live, 2, "fixture control: base's live layer must be exactly two partitions")

		seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
			branchEngineCache(cacheDir, branch, format, 0))
		require.NoError(t, err)
		require.Len(t, seeded, 2, "CONTROL: a writable destination must receive both live partitions")
		require.ElementsMatch(t, live,
			branchBucketIDs(cacheDir, branch, format),
			"CONTROL: and they must actually land on disk")
	})
}
