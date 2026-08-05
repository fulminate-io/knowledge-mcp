// SPDX-License-Identifier: Apache-2.0

// replace_baseline_test.go — the pre-edit parse baseline, from both sides.
//
// The pair here is deliberate and neither half stands alone. One test proves a
// file that was ALREADY ungrammatical is declined and LOCATED rather than
// blamed; the other proves a file the edit genuinely broke is still rejected.
// Without the second, "never reject anything" would satisfy the first.

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

// preexistingPaths projects a result's pre-existing failures down to their
// paths, for assertions about WHICH files were declined.
func preexistingPaths(res ReplaceResult) []string {
	out := make([]string, 0, len(res.PreexistingParseFailures))
	for _, f := range res.PreexistingParseFailures {
		out = append(out, f.Path)
	}
	return out
}

// baselineDirtyErrorLine is the 1-based line carrying the syntax error in the
// dirty fixture below: `func B( {` never parses. Named so the assertion reads
// against a stated constant rather than a bare number.
const baselineDirtyErrorLine = 7

// baselineDirtyFixture places its error on baselineDirtyErrorLine, four lines
// below the deferred Close the pattern matches — so the reported location can
// only come from finding the actual error node, not from the match site.
const baselineDirtyFixture = `package main

func A() {
	defer x.Close()
}

func B( {
	return
}
`

// baselineCleanFixture is byte-identical to the dirty one up to the broken
// function, and parses.
const baselineCleanFixture = `package main

func A() {
	defer x.Close()
}
`

// TestReplace_PreexistingFailureNamesErrorLine pins that a declined file is
// reported WITH the location of the error that declined it, and that the clean
// sibling is absent from that list — the negative control that stops the gate
// passing on an implementation which simply reports every file.
func TestReplace_PreexistingFailureNamesErrorLine(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"clean.go": baselineCleanFixture,
		"dirty.go": baselineDirtyFixture,
	})
	raws := matchFixture(t, dir)
	require.Len(t, matchedFiles(raws), 2, "setup: both files must match")

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, raws, deferClosePattern, true, nil)
	require.NoError(t, err)

	require.Len(t, res.PreexistingParseFailures, 1,
		"exactly one of the pair was already broken")
	got := res.PreexistingParseFailures[0]
	assert.Equal(t, "dirty.go", got.Path)
	assert.Equal(t, baselineDirtyErrorLine, got.Line,
		"the entry names the line the error is actually on, which is what makes it actionable")
	assert.Positive(t, got.Column,
		"and a column, so the location is a point rather than a line number")

	// NEGATIVE CONTROL: the clean sibling is neither declined nor rejected.
	assert.NotContains(t, preexistingPaths(res), "clean.go",
		"a clean file must not be reported as pre-existing-broken")
	assert.NotContains(t, res.RejectedFiles, "clean.go")
}

// TestReplace_EditCausedBreakageStillRejected is the known positive that keeps
// the baseline from degenerating into never-reject-anything: a file that parses
// CLEAN before the edit and is broken BY the replacement still lands in
// RejectedFiles, is absent from PreexistingParseFailures, and is never written.
func TestReplace_EditCausedBreakageStillRejected(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{"main.go": baselineCleanFixture})
	raws := matchFixture(t, dir)
	require.NotEmpty(t, raws, "setup: the pattern must match")

	// The replacement injects an unbalanced brace, so the rewritten source —
	// and only the rewritten source — fails to parse.
	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, raws, "func(){ $X.Close()", false, nil)
	require.NoError(t, err, "a per-file reject is reported, not errored")

	assert.Contains(t, res.RejectedFiles, "main.go",
		"breakage the edit caused is still charged to the edit")
	assert.Empty(t, res.PreexistingParseFailures,
		"the file parsed clean before the edit, so it is not a pre-existing failure")

	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, baselineCleanFixture, string(onDisk), "a rejected file must never be written")

	// Same fixture, same probe, a grammatical replacement: not rejected. Without
	// this rung the assertion above cannot tell a working gate from one that
	// rejects everything.
	dir2 := fixtureRepo(t, map[string]string{"main.go": baselineCleanFixture})
	raws2 := matchFixture(t, dir2)
	ok, err := ApplyReplace(context.Background(), dir2, treesitter.LangGo, raws2, "safeClose($X)", true, nil)
	require.NoError(t, err)
	assert.Empty(t, ok.RejectedFiles, "control: a grammatical rewrite is not rejected")
	assert.Empty(t, ok.PreexistingParseFailures, "control: nor reported as pre-existing-broken")
}
