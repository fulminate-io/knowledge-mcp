// SPDX-License-Identifier: Apache-2.0

// ast_replace_wire_test.go — the replace report AS THE CALLER RECEIVES IT.
//
// The engine-level tests in internal/ast prove the counters and the pre-edit
// baseline are computed correctly. They cannot prove the handler puts them on
// the wire under the right keys: a mistyped key decodes to a zero value with no
// error, which is the failure this file exists to catch. Every assertion here
// goes through callAst and json.Unmarshal.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireCleanFile parses, and its deferred Close matches the pattern the tests
// below drive.
const wireCleanFile = `package main

func A() {
	defer x.Close()
}
`

// wireDirtyFile is wireCleanFile plus a function that never parses, on
// wireDirtyErrorLine — below the match, so a reported location can only come
// from finding the error itself.
const wireDirtyFile = `package main

func A() {
	defer x.Close()
}

func B( {
	return
}
`

const wireDirtyErrorLine = 7

// astReplaceWireFixture writes the requested files plus a go.mod into a temp
// dir, mirroring astReplaceNestFixture's shape.
func astReplaceWireFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o600))
	return dir
}

// TestAstReplace_WireReportsChangedApartFromMatched drives the two templates
// through the handler and reads the four counters off the JSON. The pair is the
// control for each other: identity must report matched-without-changed, and the
// real rewrite must report both, so neither number can be a constant.
func TestAstReplace_WireReportsChangedApartFromMatched(t *testing.T) {
	t.Run("identity_template", func(t *testing.T) {
		repoDir := astReplaceWireFixture(t, map[string]string{"main.go": wireCleanFile})
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"defer $X.Close()",
			"dry_run":false
		}`)
		require.False(t, isErr, "identity replace failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 1, out.FilesMatched, "files_matched must carry the reached file")
		assert.Equal(t, 1, out.MatchesReplaced, "matches_replaced must carry the splice")
		assert.Equal(t, 0, out.FilesChanged, "files_changed must be zero when nothing moved")
		assert.Equal(t, 0, out.MatchesChanged, "matches_changed likewise")

		onDisk, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, wireCleanFile, string(onDisk), "and the file really is unchanged")
	})

	t.Run("real_rewrite", func(t *testing.T) {
		repoDir := astReplaceWireFixture(t, map[string]string{"main.go": wireCleanFile})
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"safeClose($X)",
			"dry_run":false
		}`)
		require.False(t, isErr, "rewrite failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 1, out.FilesMatched)
		assert.Equal(t, 1, out.FilesChanged, "a real rewrite reports a changed file")
		assert.Equal(t, 1, out.MatchesReplaced)
		assert.Equal(t, 1, out.MatchesChanged, "and a changed splice")
	})
}

// TestAstReplace_WireReportsPreexistingParseFailures pins that a file which was
// already ungrammatical reaches the caller as a LOCATED entry under
// preexisting_parse_failures — and not as a rejection.
func TestAstReplace_WireReportsPreexistingParseFailures(t *testing.T) {
	repoDir := astReplaceWireFixture(t, map[string]string{
		"clean.go": wireCleanFile,
		"dirty.go": wireDirtyFile,
	})
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true}

	body, isErr, _ := callAst(t, deps, `{
		"operation":"replace",
		"language":"go",
		"pattern":"defer $X.Close()",
		"replacement":"safeClose($X)",
		"dry_run":false
	}`)
	require.False(t, isErr, "replace failed: %s", body)

	var out replaceResultShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	require.Len(t, out.PreexistingParseFailures, 1, "the already-broken file is disclosed")
	got := out.PreexistingParseFailures[0]
	assert.Equal(t, "dirty.go", got.Path)
	assert.Equal(t, wireDirtyErrorLine, got.Line, "with the line the error is on")
	assert.Positive(t, got.Column, "and a column")

	assert.NotContains(t, out.RejectedFiles, "dirty.go",
		"a pre-existing failure is not a rejection")
	assert.Equal(t, 1, out.FilesChanged, "the clean sibling is still rewritten")

	// The declined file was not written, and the rewritten one was — the
	// known positive that keeps the disclosure from being cost-free silence.
	dirtyOnDisk, err := os.ReadFile(filepath.Join(repoDir, "dirty.go"))
	require.NoError(t, err)
	assert.Equal(t, wireDirtyFile, string(dirtyOnDisk), "a declined file is never written")

	cleanOnDisk, err := os.ReadFile(filepath.Join(repoDir, "clean.go"))
	require.NoError(t, err)
	assert.Contains(t, string(cleanOnDisk), "safeClose(x)", "the clean sibling was rewritten")
}
