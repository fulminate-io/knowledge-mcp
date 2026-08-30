// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// TestStaleMergeScratchIsSweptBeforeEachMerge pins the ruled disposition of the
// stale-scratch problem: the sweep runs PER MERGE, inside the merge path, and a
// sweep it cannot complete is the MERGE'S ERROR rather than a log line.
//
// WHY PER-MERGE AND NOT AT CONSTRUCTION. A crash between a scratch file's
// creation and its unlink strands a file the size of a merged segment, and
// nothing else ever reaps it: the scratch directory is a SUBDIRECTORY of the L2
// cache root and the cache's accounting skips directories, so neither the byte
// budget nor the eviction loop can see it. Construction was the obvious place to
// sweep, but the pool constructors return a manager and no error, so a failure
// there could only have been logged and stepped past. In the merge path it
// propagates to the caller.
//
// THE SWEEP IS SCOPED BY OWNERSHIP, NOT BY EMPTYING THE DIRECTORY, and the
// concurrency leg below is what holds that line. Several merges of one engine
// share the scratch directory, so a sweep that removed everything it found would
// delete a sibling merge's output mid-write.
func TestStaleMergeScratchIsSweptBeforeEachMerge(t *testing.T) {
	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "scratchSweep"

	// Build the pool so its cache root and scratch directory exist, and seal a
	// segment so the second pass below has something to consolidate.
	dm := mgr.bm25ManagerFor(gt, name)
	require.NotNil(t, dm)
	cacheRoot := graphCacheDirFor(mgr.cacheDir, gt, name, bm25.New().Name())
	scratch := mergeScratchDir(cacheRoot)
	require.NoError(t, os.MkdirAll(scratch, 0o750))

	docs := bm25FieldDocs(2048)
	half := len(docs) / 2
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[:half]))

	// (a) A STALE FILE PLANTED BEFORE A MERGE IS GONE AFTER IT.
	stale := filepath.Join(scratch, "merge-stale-1234.seg")
	require.NoError(t, os.WriteFile(stale, []byte("orphaned by a crash"), 0o600))

	// (b) THE SCOPE FENCE, planted in the same run. A sweep that reached beyond
	// its own directory would be catastrophic and would look exactly like a pass.
	realSeg := filepath.Join(cacheRoot, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.seg")
	require.NoError(t, os.WriteFile(realSeg, []byte("a stored segment, not scratch"), 0o600))
	siblingRoot := graphCacheDirFor(mgr.cacheDir, gt, "otherPool", bm25.New().Name())
	siblingScratch := mergeScratchDir(siblingRoot)
	require.NoError(t, os.MkdirAll(siblingScratch, 0o750))
	siblingFile := filepath.Join(siblingScratch, "merge-sibling-9999.seg")
	require.NoError(t, os.WriteFile(siblingFile, []byte("another pool's in-flight merge"), 0o600))

	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[half:]))

	require.NoFileExists(t, stale, "the merge did not sweep the stale scratch file it found")
	require.FileExists(t, realSeg,
		"the sweep reached OUT of the scratch directory and removed a stored segment — that is the whole corpus at risk")
	require.FileExists(t, siblingFile,
		"the sweep reached into ANOTHER pool's scratch directory and removed a file that pool may be writing")

	// (c) The directory itself survives: the next merge's CreateTemp needs it.
	info, err := os.Stat(scratch)
	require.NoError(t, err, "the sweep removed the scratch directory itself")
	require.True(t, info.IsDir())
}

// TestAMergeScratchSweepFailureIsTheMergesError pins the ruled ERROR POSTURE: a
// sweep that cannot complete fails the merge loudly rather than being logged and
// stepped past.
//
// IT DRIVES A REAL FAILURE THROUGH THE REAL PATH rather than a fake: the scratch
// directory is made unwritable with a stale file already inside it, so the sweep's
// own os.Remove fails with a genuine permission error. Asserting on a log line
// would prove nothing about what the caller sees, which is the entire point of
// the ruling.
func TestAMergeScratchSweepFailureIsTheMergesError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory would not refuse the sweep's Remove")
	}

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "scratchSweepFails"

	dm := mgr.bm25ManagerFor(gt, name)
	require.NotNil(t, dm)
	cacheRoot := graphCacheDirFor(mgr.cacheDir, gt, name, bm25.New().Name())
	scratch := mergeScratchDir(cacheRoot)
	require.NoError(t, os.MkdirAll(scratch, 0o750))

	docs := bm25FieldDocs(2048)
	half := len(docs) / 2
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[:half]))

	stale := filepath.Join(scratch, "merge-stale-4321.seg")
	require.NoError(t, os.WriteFile(stale, []byte("orphaned by a crash"), 0o600))

	// Readable and traversable so the sweep can LIST the directory, but not
	// writable, so removing an entry from it is refused.
	// G302 wants 0600-or-less, which is a FILE rule; this is a DIRECTORY and needs
	// its execute bit to stay listable, which is the whole point — the sweep must be
	// able to READ the directory and be refused when it tries to REMOVE from it.
	require.NoError(t, os.Chmod(scratch, 0o500))       //nolint:gosec // G302: directory, not a file; see above
	t.Cleanup(func() { _ = os.Chmod(scratch, 0o750) }) //nolint:gosec // G302: directory, not a file

	err := mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[half:])
	require.Error(t, err,
		"a merge whose stale-scratch sweep failed reported success — the ruled posture is that the failure is the MERGE'S error")
	// THE ASSERTION NAMES THE SWEEP SPECIFICALLY, and that precision is load-bearing
	// rather than fussy. An unwritable scratch directory also fails the subsequent
	// os.CreateTemp, whose error likewise contains "merge scratch" — so a looser
	// match passes even when the sweep's error has been swallowed entirely.
	// Measured: with the sweep's Remove error discarded, a substring check on
	// "merge scratch" still goes green, because CreateTemp then supplies the
	// failure. Matching the sweep's own wording is what tells the two apart.
	require.Contains(t, err.Error(), "removing stale merge scratch",
		"the error is not the SWEEP's — a swallowed sweep failure followed by some other "+
			"scratch error would read the same to an operator, got: %v", err)

	// KNOWN POSITIVE, same run: with the permission restored the same call
	// succeeds, so the error above is attributable to the sweep rather than to a
	// fixture that could never have merged.
	require.NoError(t, os.Chmod(scratch, 0o750)) //nolint:gosec // G302: directory, not a file
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[half:]),
		"control: with the directory writable the same merge must succeed")
	require.NoFileExists(t, stale, "the recovered merge must have swept the stale file")
}
