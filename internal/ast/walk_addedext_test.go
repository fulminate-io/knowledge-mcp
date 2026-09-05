// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAstWalkAddedExtensions is the SECOND consumer of the extension table.
// DetectLanguage returning javascript for a .cjs file proves nothing about this
// package: the language-scoped walk filters candidates by DetectLanguage itself,
// so the file only becomes reachable to match/count/replace once the table
// routes it. Asserting the detection alone would leave this half unmeasured.
func TestAstWalkAddedExtensions(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	write("plain.js", "function a() { return 1; }\n")
	write("bundle.cjs", "module.exports = function b() { return 2; };\n")
	// KNOWN-NEGATIVE CONTROL: a file of another language in the same tree must
	// NOT come back for javascript, or a walk that returned everything
	// regardless of language would satisfy the assertion below.
	write("other.py", "def c():\n    return 3\n")

	files, _, _, err := discoverScopedFiles(context.Background(), dir, "javascript", Scope{IncludeTests: true})
	require.NoError(t, err)

	require.Contains(t, files, "bundle.cjs",
		"the language-scoped walk must return a .cjs file for javascript")
	require.Contains(t, files, "plain.js",
		"known-positive control: the walk found the tree at all")
	require.False(t, slices.Contains(files, "other.py"),
		"the walk must still filter by language")
}
