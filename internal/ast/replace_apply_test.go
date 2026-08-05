// SPDX-License-Identifier: Apache-2.0

// replace_apply_test.go — ApplyReplace orchestrator coverage: dry-run leaves
// disk untouched, apply rewrites atomically with no
// leftover .tmp, a re-parse-failing file lands in RejectedFiles and is never
// written, and a second apply matches nothing (idempotency). Split from
// replace_test.go to stay under the line cap.

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
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()
	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
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

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "safeClose($X)", true, nil)
	require.NoError(t, err)

	assert.False(t, res.Applied, "dry run must report Applied=false")
	assert.Equal(t, 1, res.FilesMatched)
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

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "safeClose($X)", false, nil)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, 1, res.FilesMatched)

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
	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "func(){ $X.Close()", false, nil)
	require.NoError(t, err, "a per-file reject is reported, not errored")
	assert.Contains(t, res.RejectedFiles, "main.go")
	// A rejected file is neither matched nor changed: the fixture parses CLEAN
	// before the edit and is broken only by the replacement, so it stays on the
	// rejected path rather than the pre-existing-failure one.
	assert.Equal(t, 0, res.FilesMatched, "a rejected file is not counted as matched")
	assert.Equal(t, 0, res.FilesChanged, "nor as changed")
	assert.Empty(t, res.PreexistingParseFailures, "and it was not broken before the edit")

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
	res1, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches1, "safeClose($X)", false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res1.FilesChanged, "the first apply really rewrote the file")

	// Re-match against the rewritten tree: the replacement no longer matches
	// the pattern, so zero matches -> zero edits -> FilesMatched==0.
	matches2 := matchFixture(t, dir)
	assert.Empty(t, matches2, "rewritten source must no longer match defer Close")
	res2, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches2, "safeClose($X)", false, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.FilesMatched, "idempotency: a second apply matches nothing")
	assert.Equal(t, 0, res2.FilesChanged, "and therefore changes nothing")
}

// TestReplace_IdentitySplicesCountAsMatchedNotChanged pins the split between
// what the pattern reached and what actually moved.
//
// The two legs run the SAME fixture through the SAME probe and differ only in
// the template, so neither number can be hardcoded: an implementation that
// pinned changed to zero would fail the rewrite leg, and one that mirrored
// matched into changed would fail the identity leg.
func TestReplace_IdentitySplicesCountAsMatchedNotChanged(t *testing.T) {
	const src = `package main

func A() {
	defer x.Close()
	defer y.Close()
}
`

	t.Run("identity_template_matches_without_changing", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": src})
		matches := matchFixture(t, dir)
		require.Len(t, matches, 2, "setup: two splices to count")

		// Template == pattern, so the source-anchored splice reproduces the
		// matched bytes exactly.
		res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, deferClosePattern, false, nil)
		require.NoError(t, err)

		assert.Equal(t, 1, res.FilesMatched, "the file was matched")
		assert.Equal(t, 2, res.MatchesReplaced, "both splices were applied")
		assert.Equal(t, 0, res.FilesChanged, "but nothing in it moved")
		assert.Equal(t, 0, res.MatchesChanged, "and no splice differed from what it replaced")
		assert.Empty(t, res.Diffs["main.go"], "an identity rewrite has an empty diff — the report now says so too")

		onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, src, string(onDisk), "an identity apply is a byte-identical no-op")
	})

	t.Run("real_rewrite_moves_both_pairs", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": src})
		matches := matchFixture(t, dir)
		require.Len(t, matches, 2)

		res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, matches, "safeClose($X)", false, nil)
		require.NoError(t, err)

		assert.Equal(t, 1, res.FilesMatched)
		assert.Equal(t, 1, res.FilesChanged, "a real rewrite changes the file it matched")
		assert.Equal(t, 2, res.MatchesReplaced)
		assert.Equal(t, 2, res.MatchesChanged, "and every splice moved bytes")
		assert.NotEmpty(t, res.Diffs["main.go"])
	})
}
