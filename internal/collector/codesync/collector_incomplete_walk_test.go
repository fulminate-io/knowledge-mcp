// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// gitFixtureRepo initializes a git repository at dir and commits everything in
// it, so discovery takes the git path (git ls-files) rather than the
// filesystem-walk fallback — the path a real collect runs.
func gitFixtureRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "fixture@example.invalid"},
		{"config", "user.name", "fixture"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// TestCodeCollector_IncompleteWalkFailsCollect drives the REAL code collector
// over a fixture repo. An incomplete walk fails the collect client-side: a file
// chunking could not read SHOULD have nodes and its absence is TRANSIENT, so
// proceeding would either under-index the repository or name a live file as a
// deletion.
//
// TWO SUBTESTS, EACH KILLING A DIFFERENT DEGENERATE IMPLEMENTATION. A collector
// that always succeeds dies on the first; one that always errors dies on the
// second. Neither alone is sufficient, because the cheapest way to satisfy the
// first is to fail every collect.
//
// THE FIXTURE IS A SYMLINK TO A DIRECTORY, and it is neither chmod nor a
// git-tracked-but-deleted file. A chmod-000 file is still readable BY ROOT and
// CI may run as root, so it would pass for the wrong reason on exactly the
// machine that matters. A tracked-but-deleted file never reaches chunking at
// all: isIndexable stats every candidate and DECLINES a path that fails to stat,
// charging it to skip_too_large (indexer_discover.go), so discovery drops it as
// a rule-based exclusion and the collect legitimately succeeds — measured, not
// assumed. A symlink to a directory stats fine, carries an indexable extension,
// and fails os.ReadFile with EISDIR at the production read site, so it drives
// the real chunk_read_error path root-independently and deterministically.
func TestCodeCollector_IncompleteWalkFailsCollect(t *testing.T) {
	t.Run("unreadable_file_FAILS_the_collect", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "present.go"), "package p\n\nfunc P() {}\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "hole"), 0o750))
		require.NoError(t, os.Symlink("hole", filepath.Join(dir, "unreadable.go")))
		gitFixtureRepo(t, dir)

		result, err := (&CodeCollector{}).Collect(newTestCtx(t), dir, collector.CollectOptions{})
		require.Error(t, err, "a dropped file must FAIL the collect, never degrade it")
		require.Nil(t, result, "a failed collect returns no result to upload")
		require.Contains(t, err.Error(), "unreadable.go",
			"the error must NAME the dropped file — an error saying only 'incomplete walk' leaves the operator exactly as stuck as the silent fallback did")
	})

	t.Run("clean_walk_collects", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "present.go"), "package p\n\nfunc P() {}\n")
		writeFile(t, filepath.Join(dir, "other.go"), "package p\n\nfunc O() {}\n")
		gitFixtureRepo(t, dir)

		result, err := (&CodeCollector{}).Collect(newTestCtx(t), dir, collector.CollectOptions{})
		require.NoError(t, err, "an ordinary repository must still collect")
		require.NotEmpty(t, result.Nodes, "control: the clean walk produced nodes")
		require.True(t, result.WalkComplete,
			"and asserts a complete walk on the wire, computed from the same report the failure above reads")
	})
}
