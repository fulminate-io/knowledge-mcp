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

// manager_seed_ship_test.go — the cloud half of the branch seed: the copied
// partitions are uploaded under the BRANCH's own object key, and a ship that
// confirmed less than everything publishes nothing at all.

// seedShipFixture plants two published base partitions and returns a Manager
// wired to the package's recording source. drop names an id the ship must refuse
// to confirm, or "" for a clean ship.
//
// IT ALSO RETURNS THE BRANCH'S CACHE, and returning ONE instance for both halves
// is the point rather than a convenience: in production the copy destination and
// the ship's read source are the same engine-owned instance, and a fixture that
// handed each half its own would be testing a topology the code no longer has.
func seedShipFixture(t *testing.T, drop searchengine.SegmentID) (
	*Manager, *recordingSource, string, string, string, *diskSegmentCache,
) {
	t.Helper()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const repo = "ship-repo"
	const branch = "ship-repo@feature"
	plantBlob(t, cacheDir, repo, format, "seg-a", []byte("alpha payload"))
	plantBlob(t, cacheDir, repo, format, "seg-b", []byte("beta payload"))
	src := &recordingSource{
		listMetas: []searchengine.SegmentMeta{
			{ID: "seg-a", Format: format, DocCount: 7},
			{ID: "seg-b", Format: format, DocCount: 11},
		},
		dropOnShip:         drop,
		verifiesServerSide: true,
	}
	return closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, cacheDir, 0, withSegmentSource(src))), src, repo, branch, format,
		branchEngineCache(cacheDir, branch, format, 0)
}

// TestSeedShip_BranchPublishesSeededDigestsUnderBranchKey asserts the seeded
// partitions are shipped under the BRANCH key and then published with their doc
// counts, which is what makes the branch's shipped doc count non-zero.
//
// THE TARGET IS ASSERTED, NOT JUST THE COUNT. This whole step exists because blob
// object paths carry the graph NAME, so base's bytes are unreachable under a
// branch name; a test that only counted shipped blobs would pass against an
// upload aimed at the wrong graph, which is the exact defect.
func TestSeedShip_BranchPublishesSeededDigestsUnderBranchKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, src, repo, branch, format, branchCache := seedShipFixture(t, "")

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format, branchCache)
	require.NoError(t, err)
	require.Len(t, seeded, 2, "fixture control: both published partitions must have been copied")

	require.NoError(t, mgr.seedShipAndPublish(ctx, kgtypes.GraphCode, branch, format, seeded, branchCache))

	targets, shipped := src.shipRecord()
	require.ElementsMatch(t, []searchengine.SegmentID{"seg-a", "seg-b"}, shipped,
		"every seeded partition must be shipped")
	require.NotEmpty(t, targets, "the ship must actually have run")
	for _, target := range targets {
		require.Contains(t, target, branch,
			"the ship must target the BRANCH key, not %q — byte-identical content under a different graph "+
				"name is a different object, so base's blobs are unreachable from the branch", target)
	}
	require.Equal(t, 1, src.publishCalls, "the seeded digests must be published exactly once")
	require.ElementsMatch(t,
		[]segmentDigest{{ID: "seg-a", DocCount: 7}, {ID: "seg-b", DocCount: 11}}, src.published,
		"the publish must carry each digest's doc count, which is the denominator a coverage read needs")
}

// TestSeedShip_PartialShipDoesNotPublish drives a Ship that OMITS a digest and
// asserts NO publish follows.
//
// THIS IS THE ONE THAT ESTABLISHES THE REFUSAL. The structural gate can only see
// that the guard is written; this observes that it decides. Publishing what came
// back would declare the bucket complete while it is missing a partition — and a
// bucket that reads shipped-complete while missing documents is precisely what
// the downstream single-pool gate would believe.
func TestSeedShip_PartialShipDoesNotPublish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, src, repo, branch, format, branchCache := seedShipFixture(t, "seg-b")

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format, branchCache)
	require.NoError(t, err)
	require.Len(t, seeded, 2, "fixture control: both partitions must be seeded, so the omission is the ship's")

	err = mgr.seedShipAndPublish(ctx, kgtypes.GraphCode, branch, format, seeded, branchCache)
	require.Error(t, err, "an incomplete ship must fail loudly rather than publish what landed")
	require.ErrorContains(t, err, "refusing to publish")

	require.Zero(t, src.publishCalls, "NO publish may follow an incomplete ship")
	require.Empty(t, src.published)
	// KNOWN-POSITIVE CONTROL on the same probe: the ship really did run and really
	// did confirm the OTHER partition. Without it, "zero publishes" would also be
	// satisfied by a ship that never happened at all.
	_, shipped := src.shipRecord()
	require.Equal(t, []searchengine.SegmentID{"seg-a"}, shipped,
		"control: the ship ran and confirmed the partition it did not drop")
}
