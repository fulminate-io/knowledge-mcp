// SPDX-License-Identifier: Apache-2.0

// ast_include_tests_test.go — the include_tests refusal for a language ast
// carries no test-file convention for.
//
// The asymmetry under test is the whole point: SUPPLYING the flag for such a
// language is an error naming it, while OMITTING it is an ordinary call. That
// distinction only exists because astArgs.IncludeTests is a pointer; a plain
// bool would make every omission indistinguishable from an explicit false and
// so make every Rust call an error.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rustFixture writes a two-file Rust tree: one library file and one integration
// test under Cargo's tests/ directory. Both must be walked, because Rust's
// disposition is nil — no filename tells them apart.
func rustFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	lib := "fn alpha() {\n    let _ = beta();\n}\n"
	integration := "fn gamma() {\n    let _ = beta();\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(lib), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tests", "integration.rs"), []byte(integration), 0o600))
	return dir
}

// TestAstIncludeTests_UnsupportedLanguageErrorsOnlyWhenSupplied pins both legs
// of the asymmetry plus the Go control that proves the flag is still accepted
// where a convention exists.
func TestAstIncludeTests_UnsupportedLanguageErrorsOnlyWhenSupplied(t *testing.T) {
	rustDir := rustFixture(t)
	rustDeps := astTestDeps{rootDir: rustDir, rootDirSet: true}

	// SUPPLIED for a language with no convention → refused, naming the language.
	for _, supplied := range []string{"true", "false"} {
		body, isErr, _ := callAst(t, rustDeps, `{
			"operation":"match",
			"language":"rust",
			"pattern":"beta()",
			"include_tests":`+supplied+`
		}`)
		require.True(t, isErr, "include_tests:%s on rust must be refused, got: %s", supplied, body)
		t.Logf("refusal (include_tests:%s): %s", supplied, body)
		assert.Contains(t, body, "rust", "the refusal must NAME the language it refused")
		assert.Contains(t, body, "include_tests", "the refusal must name the parameter it refused")
		// It must also be actionable: a caller is told which languages do carry a
		// convention rather than left to probe them one at a time.
		assert.Contains(t, body, "go", "the refusal must name languages that do support the flag")
	}

	// OMITTED → an ordinary call. This is the leg a plain bool would break, and
	// it is a KNOWN POSITIVE for the refusal above: without it, a handler that
	// rejected every rust call would pass the first leg.
	body, isErr, _ := callAst(t, rustDeps, `{
		"operation":"match",
		"language":"rust",
		"pattern":"beta()"
	}`)
	require.False(t, isErr, "omitting include_tests must not error on rust: %s", body)
	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	// Both files are walked: Rust has no convention, so nothing is filtered out.
	assert.Equal(t, 2, out.Stats.FilesScanned, "with no convention there is nothing to exclude, so both files are walked")
	paths := map[string]bool{}
	for _, m := range out.Matches {
		paths[m.FilePath] = true
	}
	assert.True(t, paths["lib.rs"], "the library file matched")
	assert.True(t, paths[filepath.Join("tests", "integration.rs")], "and so did the file under tests/, which no predicate claims")

	// CONTROL: the same flag on a language that HAS a convention is accepted and
	// acts. Without this the refusal could be a blanket "include_tests is dead".
	goDeps := astTestDeps{rootDir: astIntegrationFixture(t), rootDirSet: true}
	goBody, goIsErr, _ := callAst(t, goDeps, `{
		"operation":"match",
		"language":"go",
		"pattern":"defer $X.Close()",
		"include_tests":true
	}`)
	require.False(t, goIsErr, "include_tests is supported for go: %s", goBody)
	var goOut matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(goBody), &goOut))
	sawTest := false
	for _, m := range goOut.Matches {
		if strings.HasSuffix(m.FilePath, "_test.go") {
			sawTest = true
		}
	}
	assert.True(t, sawTest, "include_tests:true on go admits the _test.go file, so the accepted flag is doing something")
}

// TestAstIncludeTests_RefusalReachesTheWritePath pins the refusal on replace,
// where an inert blast-radius control is at its most expensive: the caller
// believes test files are excluded while the rewrite touches them.
func TestAstIncludeTests_RefusalReachesTheWritePath(t *testing.T) {
	dir := rustFixture(t)
	deps := astTestDeps{rootDir: dir, rootDirSet: true}

	body, isErr, _ := callAst(t, deps, `{
		"operation":"replace",
		"language":"rust",
		"pattern":"beta()",
		"replacement":"delta()",
		"include_tests":false,
		"dry_run":true
	}`)
	require.True(t, isErr, "a replace scoped with an unsupported include_tests must be refused, got: %s", body)
	assert.Contains(t, body, "rust")

	// And the files on disk are untouched, since the refusal happens before any
	// walk — a dry run would not have written either, so this asserts the
	// refusal, not dry_run's own guarantee.
	original, err := os.ReadFile(filepath.Join(dir, "lib.rs"))
	require.NoError(t, err)
	assert.Contains(t, string(original), "beta()")
}
