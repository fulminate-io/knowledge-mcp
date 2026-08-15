// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// captureRepoContext registers a PROBE BindsResolver arm that records the
// RepoContext the binds pass hands it, runs Populate over dir, and returns what
// the arm saw.
//
// THE PROBE IS REGISTERED FOR PYTHON, NOT GO, and that choice is load-bearing
// rather than incidental: Populate must hand the RepoContext to an arm, and
// python is a language whose fixture this file already writes.
//
// THE CLEANUP RESTORES RATHER THAN UNREGISTERS, and that is a correction rather
// than a preference. UnregisterBindsResolver DELETES the registry entry, so a
// cleanup that unregisters a language which HAS a real arm removes it for every
// later test in the same binary — the symptom being resolution tests that pass
// alone and fail in the package. Python HAS a production arm now, so the probe
// is swapped in and the real registrations are reinstalled on the way out.
//
// The probe observes the value under test directly: rc is built inside Populate
// and handed to the pass, and this is the seam where a caller can see it.
//
// IT KEEPS THE POINTER RATHER THAN DEREFERENCING IT. RepoContext carries the
// per-collect derivation caches the rust and JVM arms fill lazily, so copying
// one out of the probe would fork those caches — and the sync primitives
// guarding them make the copy a vet failure besides.
func captureRepoContext(t *testing.T, dir string) (*treesitter.RepoContext, map[string]*treesitter.Result) {
	t.Helper()

	var seen *treesitter.RepoContext
	var byPath map[string]*treesitter.Result
	treesitter.RegisterBindsResolver(treesitter.LangPython,
		func(rc *treesitter.RepoContext, all map[string]*treesitter.Result, _ *treesitter.Result) treesitter.BindsResult {
			if rc != nil {
				seen = rc
			}
			byPath = all
			return treesitter.BindsResult{}
		})
	t.Cleanup(treesitter.RegisterLanguageBindsResolvers)

	_, err := Populate(context.Background(), "probe", dir)
	require.NoError(t, err)
	require.NotNil(t, byPath, "the probe arm was never invoked, so nothing below was actually observed")
	require.NotNil(t, seen, "the probe arm saw no RepoContext, so every field assertion below would be vacuous")
	return seen, byPath
}

// writeFixtureFile writes rel under dir, creating parent directories.
func writeFixtureFile(t *testing.T, dir, rel, src string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
	require.NoError(t, os.WriteFile(full, []byte(src), 0o600))
}

// TestPopulateBuildsRepoContext pins the RepoContext Populate hands to the binds
// pass. ModulePath is the field no other language arm reads, which is exactly
// why it is the one likeliest to land declared-but-never-filled: a zero
// ModulePath is not a compile error and reds no gate elsewhere — it silently
// makes a module-path-consuming arm return its zero result on every repo.
func TestPopulateBuildsRepoContext(t *testing.T) {
	t.Run("with_go_mod", func(t *testing.T) {
		dir := t.TempDir()
		writeFixtureFile(t, dir, "go.mod", "module example.com/probe\n\ngo 1.24\n")
		writeFixtureFile(t, dir, "app/main.go", "package main\n\nfunc main() {}\n")
		writeFixtureFile(t, dir, "probe/probe.py", "def hello():\n    return 1\n")
		// A file that produces NO chunks. It is the discriminator between the
		// DISCOVERED file list and one derived from the chunk results: a
		// results-derived set would omit it, and a file with no chunks still
		// has a scope.
		writeFixtureFile(t, dir, "app/empty.go", "")

		rc, byPath := captureRepoContext(t, dir)

		assert.Equal(t, "example.com/probe", rc.ModulePath)
		assert.Equal(t, dir, rc.Root)
		require.NotEmpty(t, rc.Files)
		assert.Contains(t, rc.Files, "app/empty.go",
			"the DISCOVERED file list is what reaches the pass, not a set derived from the chunk results")

		// The discriminator is only a discriminator if the file really produced
		// nothing — otherwise a results-derived set would have carried it too.
		// Asserted unconditionally across both shapes the results can take:
		// omitted from the result set entirely, or present with no chunks.
		chunkless, present := byPath["app/empty.go"]
		assert.True(t, !present || len(chunkless.Chunks) == 0,
			"app/empty.go is the fixture's chunkless file, and that is what makes the Files assertion above discriminating")

		// KNOWN-POSITIVE CONTROL for the Files assertion: a file that DID
		// produce chunks is present as well, so Contains above is not passing
		// against a list of one accidental entry.
		assert.Contains(t, rc.Files, "app/main.go")
	})

	t.Run("without_go_mod", func(t *testing.T) {
		dir := t.TempDir()
		writeFixtureFile(t, dir, "app/main.go", "package main\n\nfunc main() {}\n")
		writeFixtureFile(t, dir, "probe/probe.py", "def hello():\n    return 1\n")

		rc, _ := captureRepoContext(t, dir)

		assert.Empty(t, rc.ModulePath, "a repo with no go.mod is the normal non-Go case, not a failure")
		assert.Equal(t, dir, rc.Root)
		assert.NotEmpty(t, rc.Files)
	})
}
