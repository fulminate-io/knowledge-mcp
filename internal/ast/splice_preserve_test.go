// SPDX-License-Identifier: Apache-2.0

// splice_preserve_test.go — the preservation half of the splice contract, the
// half the identity invariant structurally cannot prove.
//
// An engine that emitted no edit at all would satisfy every identity row in
// identity_splice_test.go. What the tool actually has to do is narrower and
// stronger: a template that rewrites SOME tokens must leave every OTHER byte
// inside the matched span untouched. So this test rewrites exactly one token
// of a multi-line, tab-indented Go function and asserts the FULL expected
// output bytes — not a substring, not a diff-line count — plus the negative
// form of the same claim, that the unified diff carries no -/+ pair for either
// body line.
//
// Without this row the surviving defect is an implementation that special-
// cases the identity template (template equals pattern -> emit source
// verbatim) and reflows everything else. Every identity row would pass and the
// tool would remain unusable for the thing it exists to do.

package ast

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// changedDiffLines returns every added-or-removed line of a unified diff, with
// the ---/+++ file headers dropped. The count and the content are both
// assertions: a body line appearing here at all is the reflow defect.
func changedDiffLines(diff string) []string {
	var out []string
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") {
			out = append(out, line)
		}
	}
	return out
}

// TestSpliceOneTokenRewritePreservesRest renames a function and asserts every
// other byte of the match survives: both tabs, both newlines and the brace
// placement all come from source, and only the identifier changes.
func TestSpliceOneTokenRewritePreservesRest(t *testing.T) {
	const before = "package main\n\nfunc alpha() {\n\tfirst()\n\tsecond()\n}\n"
	const after = "package main\n\nfunc renamed_alpha() {\n\tfirst()\n\tsecond()\n}\n"

	dir := fixtureRepo(t, map[string]string{"main.go": before})

	res, matches := runSplice(t, dir, treesitter.LangGo,
		"func $N() { $$$B }", "func renamed_$N() { $$$B }", false)

	require.Equal(t, 1, matches, "the fixture holds exactly one function to rewrite")
	require.Empty(t, res.RejectedFiles)
	require.Empty(t, res.RefusedFiles)
	require.Equal(t, 1, res.MatchesReplaced)

	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, after, string(onDisk),
		"a one-token rewrite must reproduce the whole match byte-for-byte apart from that token")

	// The negative form of the same claim: the body lines are untouched, so
	// they never reach the diff.
	changed := changedDiffLines(res.Diffs["main.go"])
	assert.Equal(t, []string{"-func alpha() {", "+func renamed_alpha() {"}, changed,
		"only the signature line may change; a -/+ pair for a body line is the reflow defect")
}
