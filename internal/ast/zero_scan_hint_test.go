// SPDX-License-Identifier: Apache-2.0

// zero_scan_hint_test.go — the zero-scan hint names the cause it measured.
//
// Every row here drives a REAL walk and hands the walk's own stats to the hint,
// so the cause is read off the exclusion report discovery produced rather than
// off a WalkStats a test invented. Two of the three rows fail under an
// always-blame-the-root implementation; the third is the control that keeps the
// fix from swinging to never-blame-the-root.

package ast

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// wrongRootPhrase is the text the hint used to emit for every zero-scan walk,
// whatever the cause. Two rows below assert its ABSENCE.
const wrongRootPhrase = "wrong root?"

// TestZeroScanHint_NamesTheActualCause walks three trees that each scan zero
// files for a different reason and requires the hint to say which.
func TestZeroScanHint_NamesTheActualCause(t *testing.T) {
	const body = `package p

func A() {
	defer x.Close()
}
`

	t.Run("prefix filter excluded everything", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"pkg/in.go": body})
		scope := Scope{PackagePrefixes: []string{"nosuch"}}
		_, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, scope)
		require.Equal(t, 0, stats.FilesScanned, "setup: the prefix must exclude the only file")

		hint := ZeroScanHint(dir, "go", scope, stats)
		assert.Contains(t, hint, "package_prefixes", "the prefix filter is what emptied this walk, so the hint names it")
		assert.Contains(t, hint, "nosuch", "and echoes the prefix that did it")
		assert.NotContains(t, hint, wrongRootPhrase, "the root was correct: the file is there, the prefix did not reach it")
	})

	t.Run("size rule outranks the prefix that reached the file", func(t *testing.T) {
		// The c-redis shape: a prefix naming one real file, which a discovery
		// rule then declines. Both causes are live and the RULE is the specific
		// one — the scope did reach the file.
		huge := body + "\n// " + strings.Repeat("pad", 220_000) + "\n"
		require.Greater(t, len(huge), 512*1024, "setup: fixture must exceed maxFileSize")
		dir := fixtureRepo(t, map[string]string{"huge.go": huge, "small.go": body})

		scope := Scope{PackagePrefixes: []string{"huge.go"}}
		_, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, scope)
		require.Equal(t, 0, stats.FilesScanned, "setup: the only in-scope file is over the size cap")
		require.Equal(t, 1, stats.ExcludedByRule["skip_too_large"], "setup: and discovery must have charged it to the size rule")

		hint := ZeroScanHint(dir, "go", scope, stats)
		assert.Contains(t, hint, "skip_too_large", "the size rule is the specific cause and outranks the prefix filter")
		assert.Contains(t, hint, "huge.go", "and the hint names the file it declined")
		assert.Contains(t, hint, "lift_exclusions", "with the lever that would walk it anyway")
		assert.NotContains(t, hint, wrongRootPhrase, "the root was correct")
		assert.NotContains(t, hint, "package_prefixes", "and the prefix, though supplied, is not what emptied the walk")
	})

	t.Run("wrong root keeps the wrong-root text", func(t *testing.T) {
		// A tree with no Go in it at all. The markdown file IS declined by a
		// discovery rule, which is the known negative for the rule branch: an
		// exclusion of some OTHER language must not be reported as the cause of a
		// Go walk finding nothing.
		dir := fixtureRepo(t, map[string]string{"README.md": "# docs\n"})
		scope := Scope{}
		_, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, scope)
		require.Equal(t, 0, stats.FilesScanned, "setup: there is no Go here to scan")
		require.NotEmpty(t, stats.ExcludedByRule, "setup: discovery did decline something, just not a Go file")

		hint := ZeroScanHint(dir, "go", scope, stats)
		assert.Contains(t, hint, wrongRootPhrase, "nothing of this language is under this root, which is the wrong-root case")
		assert.NotContains(t, hint, "package_prefixes", "no prefixes were supplied, so none can be blamed")
		assert.NotContains(t, hint, "skip_markdown", "a rule that declined another language's file is not the cause of this zero")
	})
}
