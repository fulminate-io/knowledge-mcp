// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// TestHealRebuildsIntoNewFormatFamily exercises the MIGRATION rather than
// describing it.
//
// There is no converter for the retired layout and there is not meant to be one.
// Segments are a derived cache, so the migration is: refuse the old blobs, and
// let the production rebuild path repopulate. This drives that path from an
// EMPTY cache — the state every graph is in on the first run after the upgrade,
// because the format name moved the whole cache tree — and requires a searchable
// corpus in the new family at the end.
//
// It also measures what the migration COSTS, because "the corpus rebuilds" is an
// adjective and the first run after an upgrade pays it for every graph.
func TestHealRebuildsIntoNewFormatFamily(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cacheDir := t.TempDir()
	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
	gt, name := kgtypes.GraphCode, "heal-family"

	// The cache starts genuinely empty — no tree for either format family.
	family := filepath.Join(cacheDir, bm25.New().Name())
	require.NoDirExists(t, family, "the fixture must start from an empty cache, or the rebuild proves nothing")

	const corpus = 512
	docs := vecContentDocs(corpus)

	start := time.Now()
	stageRebuildRun(t, ctx, mgr, gt, name, docs)
	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, res.Swapped, "the rebuild must LAND — a skipped publish also returns a nil error")
	elapsed := time.Since(start)

	// (1) SEARCHABLE. The rebuilt corpus answers queries, which is the only
	// property that makes the migration a migration rather than a deletion.
	dm := mgr.bm25ManagerFor(gt, name)
	// "alpha" is a term every fixture document carries, so a corpus that
	// rebuilt at all must match it.
	hits := dm.engine.Search(bm25.NewQuery("alpha"), 10)
	require.NotEmpty(t, hits,
		"the rebuilt corpus is not searchable — a heal that publishes nothing readable has migrated nothing")

	// (2) IN THE NEW FAMILY. Every meta the manager publishes carries the
	// version-carrying name, and the on-disk tree is under it.
	for _, blob := range dm.engine.Export() {
		require.Equal(t, bm25.New().Name(), blob.Format,
			"a rebuilt segment was published under the retired format name")
	}
	require.DirExists(t, family, "the rebuilt corpus must live under the new format's cache tree")
	require.NoDirExists(t, filepath.Join(cacheDir, retiredBM25Tree),
		"a fresh rebuild must not create the retired tree")

	// (3) THE COST, as a number rather than an adjective. Recorded rather than
	// bounded: this is one machine and a fixture corpus, so a threshold here
	// would be a flake generator, but an operator planning an upgrade is
	// entitled to the shape of the number.
	var bytes int64
	_ = filepath.Walk(family, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			bytes += info.Size()
		}
		return nil
	})
	t.Logf("heal rebuild: %d documents into the %s family in %s (%d cached bytes)",
		corpus, bm25.New().Name(), elapsed.Round(time.Millisecond), bytes)
	require.Positive(t, bytes, "the rebuild cached no bytes, so the timing above measures nothing")
}

// TestRetiredTreeSurvivesUntilTheNewFamilyExists is the migration's data-safety
// half, at the manager level rather than the helper's.
//
// The reclamation runs on BM25 manager construction. Constructing a manager for a
// cache that has a retired tree and NO new family must leave the retired tree
// alone: at that instant it is the only cached corpus on the machine, and the new
// family has produced nothing to replace it.
func TestRetiredTreeSurvivesUntilTheNewFamilyExists(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	retired := filepath.Join(cacheDir, retiredBM25Tree, "code", "legacy")
	require.NoError(t, os.MkdirAll(retired, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(retired, "legacy.seg"), []byte("v1 bytes"), 0o600))

	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
	// Constructing the manager alone must not reclaim anything: no new family
	// directory exists yet.
	_ = mgr.bm25ManagerFor(kgtypes.GraphCode, "legacy")
	require.FileExists(t, filepath.Join(retired, "legacy.seg"),
		"the retired tree was reclaimed while it was still the only cached corpus")
}

// vecContentDocsUnused keeps the import of searchengine honest if the shared
// fixture helper moves; it is referenced by the compiler only.
var _ = func() []searchengine.Document { return nil }
