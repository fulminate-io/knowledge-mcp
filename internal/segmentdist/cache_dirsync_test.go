// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

// cache_dirsync_test.go covers the step atomicWriteFile takes AFTER its rename: the
// fsync of the PARENT DIRECTORY that makes the new directory entry durable.
//
// WHY A LOST DIRECTORY ENTRY IS DATA LOSS HERE AND NOT A CACHE MISS. reclaimMerged
// writes the consolidated blob and THEN removes the constituents it replaces, so
// post-merge the .seg file this function renames into place is the only durable copy
// of those constituents' documents. Syncing the file's data blocks without syncing the
// directory entry that names them leaves a crash window in which the payload is on the
// platter and unreachable — the corpus segment is gone with no error anywhere.
//
// THE FAULT INJECTION IS A REAL SEAM, NOT A DOUBLE. The cache root is chmod'ed to
// 0o300 (write+execute, no read): creating the temp file and renaming it both still
// succeed, because those need only w+x on the directory, while os.Open of the
// directory needs r and fails with EACCES. Nothing about atomicWriteFile is stubbed —
// the production path runs end to end against a directory it genuinely cannot open.
// Each failing leg asserts the injection TOOK before relying on it, so a run where the
// permission bits do not bite (a root euid, say) fails loudly instead of passing for
// having measured nothing.
//
// EVERY BEHAVIORAL LEG RUNS BOTH DIRECTIONS. A test that only proves "an unopenable
// parent directory produces an error" is satisfiable by an atomicWriteFile that always
// errors, which would break every segment write in the product while passing this file.
//
// The file is unix-tagged because the failure it injects is a POSIX permission
// behaviour and because the windows arm of fsyncDir is a documented no-op that cannot
// fail (see dirsync_windows.go).

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// unopenableCacheRoot builds a directory that accepts creates and renames but refuses
// to be opened, and asserts that the refusal is real before handing it back. The
// restore is registered with the test so t.TempDir's own cleanup can still remove it.
func unopenableCacheRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.MkdirAll(root, 0o750))
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) }) //nolint:gosec // G302: restores the cache-root mode newDiskSegmentCache itself uses, so t.TempDir can clean up
	//nolint:gosec // G302: 0o300 IS the fault injection — a directory that still accepts creates and renames but refuses to be opened
	require.NoError(t, os.Chmod(root, 0o300))

	// KNOWN-POSITIVE FOR THE INSTRUMENT. Without this the leg below could not tell a
	// genuinely-reported dir-sync failure from a run whose injection never bit.
	d, err := os.Open(root) //nolint:gosec // the fixture's own temp dir
	if err == nil {
		_ = d.Close()
		t.Fatalf("fault injection did not take: %s is still openable at mode 0300 "+
			"(running as root?) — this leg would pass without exercising the dir sync", root)
	}
	require.ErrorIs(t, err, fs.ErrPermission)
	return root
}

// TestAtomicWriteFileReportsAFailedParentDirSync is the behavioral gate: the
// directory fsync happens, and when it fails the caller is told.
func TestAtomicWriteFileReportsAFailedParentDirSync(t *testing.T) {
	t.Parallel()

	t.Run("a_failed_dir_sync_is_returned_not_swallowed", func(t *testing.T) {
		t.Parallel()
		root := unopenableCacheRoot(t)
		path := filepath.Join(root, "abc.seg")

		err := atomicWriteFile(path, []byte("payload"))

		// Asserted on the RETURNED error, never on a log line: a Warn-and-continue is
		// precisely the outcome this test exists to reject. The server's writer
		// (cmd/knowledge-server/internal/store/atomic_write.go) may log and carry on
		// because its blobs are re-derivable; this cache's are not.
		require.Error(t, err,
			"a parent-directory fsync that fails leaves the rename undurable — "+
				"the write must report it, not return success")
		require.ErrorIs(t, err, fs.ErrPermission,
			"and the reported error must be the directory failure itself, not some other write error")

		// THE RENAME ITSELF SUCCEEDED. This distinguishes "reported a durability
		// failure" from "reported a failure to write at all" — without it, an
		// implementation that simply refused to write into an unreadable directory
		// would pass the assertions above.
		_, statErr := os.Stat(path)
		require.NoError(t, statErr,
			"the blob must already be renamed into place — the failure being reported is the "+
				"durability of that rename, not the write")
	})

	t.Run("the_other_direction_a_syncable_dir_writes_cleanly", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		path := filepath.Join(root, "abc.seg")

		require.NoError(t, atomicWriteFile(path, []byte("payload")),
			"CONTROL: a writable, openable parent directory must produce no error")

		got, err := os.ReadFile(path) //nolint:gosec // the fixture's own temp dir
		require.NoError(t, err)
		require.Equal(t, []byte("payload"), got,
			"CONTROL: and the payload must actually be on disk")
		require.NoFileExists(t, path+".tmp", "CONTROL: the temp file must not survive a clean write")
	})
}

// TestPutReportsAFailedParentDirSync drives the same failure through the PUBLIC entry
// point. atomicWriteFile is unexported and every production write reaches it through
// Put; a durability error that atomicWriteFile returns and Put drops would be invisible
// to the leg above.
func TestPutReportsAFailedParentDirSync(t *testing.T) {
	t.Parallel()

	t.Run("put_propagates_the_dir_sync_failure", func(t *testing.T) {
		t.Parallel()
		root := unopenableCacheRoot(t)
		c := newDiskSegmentCache(root, 0, adviceRandom)

		// A REAL CONTENT HASH: Put now verifies the id against the bytes, so a
		// placeholder would be refused for THAT reason and this leg would stop
		// testing the durability failure it is about.
		payload := []byte("payload")
		id := sha256Hex(payload)

		err := c.Put(id, payload)

		require.Error(t, err,
			"Put must surface a durability failure from the only remaining persistence path")
		require.ErrorIs(t, err, fs.ErrPermission)

		// AND THE ENTRY IS NOT BOOKED. A Put that indexed the segment while reporting an
		// error would tell the reclaim the blob is persisted and let it delete the
		// constituents anyway.
		require.NotContains(t, c.index, id,
			"a Put that reported an error must not have booked the id as resident")
	})

	t.Run("the_other_direction_put_succeeds_on_a_syncable_dir", func(t *testing.T) {
		t.Parallel()
		c := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)

		payload := []byte("payload")
		id := sha256Hex(payload)

		require.NoError(t, c.Put(id, payload),
			"CONTROL: a Put into a syncable directory must succeed")
		got, ok := c.Get(id)
		require.True(t, ok, "CONTROL: and the blob must be readable back")
		require.Equal(t, payload, got)
	})
}

// TestReclaimAbortsWhenTheMergedBlobCannotBeMadeDurable executes the causal claim
// atomicWriteFile's own comment makes — that a caller told the write failed "does not
// go on to remove the constituents".
//
// THE TWO LEGS ABOVE FLANK THIS SEAM WITHOUT CROSSING IT. One proves Put returns the
// durability error; the landed TestReclaimAbortsWhenTheMergedBlobCannotBePersisted
// proves the reclaim aborts on a Put error, but it does so through an instrumented
// cache whose failPut short-circuits before any real write. Neither drives the real
// diskSegmentCache's real dir-sync failure through the real reclaim, so neither would
// notice if this particular error never reached the abort.
func TestReclaimAbortsWhenTheMergedBlobCannotBeMadeDurable(t *testing.T) {
	t.Parallel()

	constituents, merged := realMergeBlobs(t)
	res := searchengine.MergeResult{
		Merged:  merged,
		Removed: []searchengine.SegmentID{constituents[0].ID, constituents[1].ID},
	}

	root := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.MkdirAll(root, 0o750))
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) }) //nolint:gosec // G302: restores the cache-root mode newDiskSegmentCache itself uses, so t.TempDir can clean up
	c := newDiskSegmentCache(root, 0, adviceRandom)

	// Seed the pre-merge constituents WHILE THE DIRECTORY IS STILL SYNCABLE, so there
	// is something a botched reclaim could actually destroy. Without them the
	// "nothing was removed" assertion would hold trivially.
	for _, b := range constituents {
		require.NoError(t, c.Put(b.ID, b.Bytes),
			"fixture: seeding a constituent must succeed, or the test proves nothing")
	}

	// Now break only the DURABILITY step. Creates, renames and removes inside the
	// directory all still work at 0o300 — so a reclaim that ignored the error would
	// succeed in deleting the constituents, which is exactly the outcome being ruled out.
	//nolint:gosec // G302: 0o300 IS the fault injection — a directory that still accepts creates, renames and removes but refuses to be opened
	require.NoError(t, os.Chmod(root, 0o300))
	d, openErr := os.Open(root) //nolint:gosec // the fixture's own temp dir
	if openErr == nil {
		_ = d.Close()
		t.Fatalf("fault injection did not take: %s is still openable at mode 0300 (running as root?)", root)
	}

	newReclaimDMOverCache(t, c).reclaimMerged(res)

	for _, b := range constituents {
		require.FileExists(t, filepath.Join(root, b.ID+".seg"),
			"a merged blob that could not be made durable must abort the reclaim before a single "+
				"constituent is removed — those constituents are the only copy left")
		_, ok := c.Get(b.ID)
		require.True(t, ok, "constituent %s must still be readable after an aborted reclaim", b.ID)
	}
}

// TestAtomicWriteFileSyncOrderingShape is a SOURCE-SHAPE gate, not a behavioral one,
// and the name says so. An fsync is not observable from a Go test without an injected
// syscall seam, so the behavioral legs above can prove the parent directory is OPENED
// and that the failure is reported — they cannot prove Sync() rather than Close() is
// what is called on the handle, nor that the file's own fsync still precedes the
// rename. Asserting the calls are present and ordered in the source is the strongest
// available instrument short of building that seam. It does NOT prove the kernel
// flushed anything.
//
// This is the checked-in counterpart of the plan criterion that greps atomicWriteFile
// for `f.Sync()` before `os.Rename`; it keeps that property and adds the third step the
// criterion did not cover. The function name is matched WITH its open paren so a
// renamed sibling (atomicWriteFileParts, say) cannot open the range and let the gate
// report green against a tree where the function it names has moved away.
func TestAtomicWriteFileSyncOrderingShape(t *testing.T) {
	t.Parallel()

	body := funcBody(t, "cache.go", "func atomicWriteFile(")
	sync := mustIndexOfLine(t, body, "f.Sync()", "cache.go:atomicWriteFile")
	rename := mustIndexOfLine(t, body, "os.Rename(", "cache.go:atomicWriteFile")
	dirSync := mustIndexOfLine(t, body, "fsyncDir(", "cache.go:atomicWriteFile")

	require.Less(t, sync, rename,
		"the blob's own fsync must precede the rename, or the rename can publish a file whose data is not on disk")
	require.Less(t, rename, dirSync,
		"the parent-directory fsync must FOLLOW the rename — syncing the directory before the "+
			"rename flushes an entry the rename has not yet created")

	// The unix arm must actually sync the handle it opens. Without this, an fsyncDir
	// that opened and closed the directory would satisfy every assertion in this file.
	unixArm := funcBody(t, "dirsync_unix.go", "func fsyncDir(")
	require.Contains(t, unixArm, ".Sync()",
		"fsyncDir's unix arm must Sync the directory handle, not merely open it")
}

// funcBody extracts the source lines of the named function from a package file, from
// its `func` line to the closing brace in column zero. It fails the test when the file
// or the function is absent rather than returning an empty body that every Contains
// assertion below would read as a silent pass.
func funcBody(t *testing.T, file, funcPrefix string) string {
	t.Helper()
	raw, err := os.ReadFile(file) //nolint:gosec // a fixed file in this package's own directory
	require.NoError(t, err, "the gate must read %s; a missing file is a moved implementation, not a pass", file)

	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, funcPrefix) {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0,
		"%s no longer declares %s — the gate names a function that has moved or been renamed", file, funcPrefix)
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("%s: %s has no closing brace in column zero", file, funcPrefix)
	return ""
}

// mustIndexOfLine returns the byte offset of needle within body, failing loudly when it
// is absent so a removed call reads as a failure rather than as an ordering that
// happens to hold.
func mustIndexOfLine(t *testing.T, body, needle, where string) int {
	t.Helper()
	i := strings.Index(body, needle)
	require.GreaterOrEqual(t, i, 0, "%s no longer contains %s", where, needle)
	return i
}
