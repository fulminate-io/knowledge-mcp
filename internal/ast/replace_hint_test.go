// SPDX-License-Identifier: Apache-2.0

// replace_hint_test.go — the match-time parse-hint skip and its stale-safety
// fallback. The pair is deliberate: one proves a clean, unchanged file is NOT
// re-parsed (the optimization fires); the other proves a file mutated between
// match and replace is re-parsed anyway (the digest guard refuses a stale hint),
// so the skip can never certify bytes it did not actually parse.

package ast

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// matchWithHint runs the real Parse -> Compile -> Match pipeline for
// deferClosePattern with EmitParseHint set (the replace path), returning the raw
// matches and the per-matched-file parse hint the walk recorded.
func matchWithHint(t *testing.T, dir string) ([]RawMatch, map[string]fileParseHint) {
	t.Helper()
	pat, err := Parse(deferClosePattern)
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()
	raws, walk, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{EmitParseHint: true})
	require.NoError(t, err)
	return raws, walk.CleanHint
}

// TestReplace_BaselineSkipsCleanFiles proves the match-time hint lets the
// pre-edit baseline skip re-parsing files it already parsed clean: with a
// clean, digest-matching hint for every matched file, baselineParseFailures
// re-parses NONE of them. The nil-hint control re-parses all N — the
// known-positive that keeps filesParsed==0 from being a probe pointed at
// nothing.
func TestReplace_BaselineSkipsCleanFiles(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"a.go": baselineCleanFixture,
		"b.go": baselineCleanFixture,
	})
	raws, cleanHint := matchWithHint(t, dir)
	paths := matchedFiles(raws)
	sort.Strings(paths)
	require.Len(t, paths, 2, "setup: both files must match")
	require.Len(t, cleanHint, 2, "the walk must record a hint per matched file")

	srcByFile, err := readMatchedSources(dir, raws)
	require.NoError(t, err)

	// Skip fired: every file is hinted clean and its bytes are unchanged, so the
	// digest matches and nothing is re-parsed.
	failures, filesParsed := baselineParseFailures(context.Background(), paths, srcByFile, treesitter.LangGo, cleanHint)
	assert.Zero(t, filesParsed, "clean + matching-digest hints must skip every re-parse")
	assert.Empty(t, failures, "no matched file was broken")

	// Known-positive control: a nil hint re-parses every file. Without this the
	// zero above cannot tell a working skip from a baseline that parsed nothing.
	failuresNil, parsedNil := baselineParseFailures(context.Background(), paths, srcByFile, treesitter.LangGo, nil)
	assert.Equal(t, len(paths), parsedNil, "a nil hint must re-parse every matched file")
	assert.Empty(t, failuresNil, "the clean fixtures still carry no failures")
}

// TestReplace_StaleHintReparses proves the digest guard is stale-safe: a file
// hinted clean at match time but MUTATED on disk before replace mismatches its
// recorded digest, so the baseline re-parses it, finds it now-broken, reports it
// in PreexistingParseFailures, and never writes it. A hint that outlived its
// bytes cannot certify them clean.
func TestReplace_StaleHintReparses(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{"main.go": baselineCleanFixture})
	raws, cleanHint := matchWithHint(t, dir)
	require.NotEmpty(t, raws, "setup: the pattern must match the clean file")
	h, ok := cleanHint["main.go"]
	require.True(t, ok, "the clean file must be hinted")
	require.True(t, h.clean, "and hinted clean")
	require.Equal(t, len(baselineCleanFixture), h.size, "the hint sizes the clean bytes")

	// Mutate the file AFTER the match: append an unbalanced function so the bytes
	// no longer match the recorded size/digest AND no longer parse.
	broken := baselineCleanFixture + "\nfunc B( {\n"
	require.NotEqual(t, len(baselineCleanFixture), len(broken), "the mutation must change the byte length")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(broken), 0o600))

	// Replace with the now-STALE clean hint. The digest guard must distrust it:
	// re-parse fires, the broken file is a pre-existing failure, never written.
	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, raws, "safeClose($X)", false, cleanHint)
	require.NoError(t, err)
	assert.Contains(t, preexistingPaths(res), "main.go",
		"a stale hint must not certify a mutated file clean")
	assert.NotContains(t, res.RejectedFiles, "main.go",
		"it was broken before the edit reached it, so it is pre-existing, not edit-rejected")

	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, broken, string(onDisk), "a file declined at baseline is never written")
}
