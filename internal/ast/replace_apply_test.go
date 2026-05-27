// SPDX-License-Identifier: Apache-2.0

// replace_apply_test.go — ApplyReplace orchestrator coverage: dry-run leaves
// disk untouched (criterion e76dbfbe), apply rewrites atomically with no
// leftover .tmp, a re-parse-failing file lands in RejectedFiles and is never
// written, and a second apply matches nothing (idempotency, criterion
// d96bf169). Split from replace_test.go to stay under the line cap.

package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// deferClosePattern is the single pattern every apply test exercises.
const deferClosePattern = "defer $X.Close()"

// matchFixture runs the real Parse -> Compile -> Match pipeline for
// deferClosePattern against dir and returns the raw matches.
func matchFixture(t *testing.T, dir string) []RawMatch {
	t.Helper()
	pat, err := Parse(deferClosePattern)
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo)
	require.NoError(t, err)
	defer cp.Close()
	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{Limit: 100})
	require.NoError(t, err)
	return raws
}

func TestApplyReplace_DryRunLeavesDiskUnchanged(t *testing.T) {
	const before = `package main

func A() {
	defer x.Close()
}
`
	dir := fixtureRepo(t, map[string]string{"main.go": before})
	matches := matchFixture(t, dir)
	require.NotEmpty(t, matches)

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "safeClose($X)", true)
	require.NoError(t, err)

	assert.False(t, res.Applied, "dry run must report Applied=false")
	assert.Equal(t, 1, res.FilesTouched)
	assert.Equal(t, 1, res.MatchesReplaced)
	require.Contains(t, res.Diffs, "main.go")
	assert.Contains(t, res.Diffs["main.go"], "safeClose(x)")

	// Disk bytes unchanged.
	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, before, string(onDisk), "dry run must not write")
}

func TestApplyReplace_ApplyRewritesAtomically(t *testing.T) {
	const before = `package main

func A() {
	defer x.Close()
}
`
	dir := fixtureRepo(t, map[string]string{"main.go": before})
	matches := matchFixture(t, dir)

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "safeClose($X)", false)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, 1, res.FilesTouched)

	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "safeClose(x)")
	assert.NotContains(t, string(onDisk), "defer x.Close()")

	// No leftover .tmp file.
	_, statErr := os.Stat(filepath.Join(dir, "main.go.tmp"))
	assert.True(t, os.IsNotExist(statErr), "no leftover .tmp must survive the rename")
}

func TestApplyReplace_ReparseFailureRejectsFile(t *testing.T) {
	const before = `package main

func A() {
	defer x.Close()
}
`
	dir := fixtureRepo(t, map[string]string{"main.go": before})
	matches := matchFixture(t, dir)

	// Replacement injects an unbalanced brace -> the rewritten file fails the
	// HasError re-parse gate.
	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "func(){ $X.Close()", false)
	require.NoError(t, err, "a per-file reject is reported, not errored")
	assert.Contains(t, res.RejectedFiles, "main.go")
	assert.Equal(t, 0, res.FilesTouched, "a rejected file is not counted as touched")

	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, before, string(onDisk), "rejected file must never be written")
}

func TestApplyReplace_Idempotency(t *testing.T) {
	const before = `package main

func A() {
	defer x.Close()
}
`
	dir := fixtureRepo(t, map[string]string{"main.go": before})

	// First apply rewrites.
	matches1 := matchFixture(t, dir)
	res1, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches1, "safeClose($X)", false)
	require.NoError(t, err)
	require.Equal(t, 1, res1.FilesTouched)

	// Re-match against the rewritten tree: the replacement no longer matches
	// the pattern, so zero matches -> zero edits -> FilesTouched==0.
	matches2 := matchFixture(t, dir)
	assert.Empty(t, matches2, "rewritten source must no longer match defer Close")
	res2, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches2, "safeClose($X)", false)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.FilesTouched, "idempotency: a second apply touches nothing")
}
