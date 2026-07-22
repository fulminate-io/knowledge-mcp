// SPDX-License-Identifier: Apache-2.0

// ast_integration_test.go — end-to-end coverage for the client-side ast
// intercept. Exercises the full pipeline (Pattern parse → Compile → Match
// → Hydrate → JSON marshal) against the self-contained multi-file Go
// fixture from ast_integration_helpers_test.go. EnclosingNodeID is always
// EMPTY because the client-side intercept hydrates against
// ast.NoOpBackend (no code-graph access) — tests pin the absence rather
// than the presence.

package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAstIntegration_Match exercises a vanilla DSL pattern against the
// fixture. Verifies expected captures populate end-to-end through the
// Pattern parse → Compile → Match → Hydrate → JSON marshal pipeline.
func TestAstIntegration_Match(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation":"match",
		"language":"go",
		"pattern":"defer $X.Close()"
	}`)
	require.False(t, isErr, "match failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	// include_tests defaults to false → only main.go matches (lib_test.go
	// also has defer Close but is excluded).
	require.NotEmpty(t, out.Matches, "expected at least one defer-Close match")
	for _, m := range out.Matches {
		assert.False(t, strings.HasSuffix(m.FilePath, "_test.go"), "test files must be excluded by default")
		x, ok := m.Captures["X"]
		require.True(t, ok, "capture X must be present (file=%s)", m.FilePath)
		assert.NotEmpty(t, x.Text, "X capture text must be non-empty")
		// EnclosingNodeID is ALWAYS empty client-side (NoOpBackend).
		assert.Empty(t, m.EnclosingNodeID, "client-side intercept hydrates against NoOpBackend; EnclosingNodeID must be empty")
		assert.Empty(t, m.EnclosingSignature, "EnclosingSignature must also be empty under NoOpBackend")
	}
}

// TestAstIntegration_MatchPackagePrefixes scopes the walk to the lib/
// subdir and verifies main.go matches are excluded.
func TestAstIntegration_MatchPackagePrefixes(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	// Use a kind-only where-tree (against the built-in $match capture) to
	// find every function_declaration. This is the v2 idiom for the
	// deleted "search" op.
	body, isErr, _ := callAst(t, deps, `{
		"operation":"match",
		"language":"go",
		"pattern":"$_",
		"where":{"kind":{"of":"$match","is":"function_declaration"}},
		"package_prefixes":["lib/"]
	}`)
	require.False(t, isErr, "match failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.NotEmpty(t, out.Matches, "expected at least one match in lib/")
	for _, m := range out.Matches {
		assert.True(t, strings.HasPrefix(m.FilePath, "lib/"), "every match must be under lib/, got %s", m.FilePath)
		assert.NotEqual(t, "main.go", m.FilePath, "main.go must be excluded by package_prefixes filter")
	}
}

// TestAstIntegration_MatchExcludesTests verifies include_tests defaults
// to false: lib/lib_test.go's TestOpen does not surface in
// function_declaration results.
func TestAstIntegration_MatchExcludesTests(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	// `func $N($$$ARGS) { $$$BODY }` matches Go function_declaration nodes
	// and binds the name as $N. Skip the where-tree — pattern shape alone
	// constrains the match.
	body, isErr, _ := callAst(t, deps, `{
		"operation":"match",
		"language":"go",
		"pattern":"func $N($$$ARGS) { $$$BODY }"
	}`)
	require.False(t, isErr, "match failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.NotEmpty(t, out.Matches)
	for _, m := range out.Matches {
		assert.False(t, strings.HasSuffix(m.FilePath, "_test.go"), "test file %s must be excluded when include_tests defaults to false", m.FilePath)
		nCap, ok := m.Captures["N"]
		require.True(t, ok, "capture N must be populated (file=%s)", m.FilePath)
		assert.NotEqual(t, "TestOpen", nCap.Text, "TestOpen lives in lib_test.go and must not surface")
	}
}

// TestAstIntegration_Count verifies the count op total + by_file
// (repo-relative path keys, no absolute leak).
func TestAstIntegration_Count(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	// Kind-only where-tree counts every function_declaration in scope.
	// Wildcard pattern + $match kind gate is the v2 idiom for "find every
	// node of a given kind".
	body, isErr, _ := callAst(t, deps, `{
		"operation":"count",
		"language":"go",
		"pattern":"$_",
		"where":{"kind":{"of":"$match","is":"function_declaration"}}
	}`)
	require.False(t, isErr, "count failed: %s", body)

	var out struct {
		Total        int            `json:"total"`
		ByFile       map[string]int `json:"by_file"`
		FilesScanned int            `json:"files_scanned"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	// main.go has Main (1), lib/lib.go has Open (1; method Close is a
	// method_declaration so excluded from function_declaration count),
	// lib_test.go is excluded by default. Expect 2.
	assert.Equal(t, 2, out.Total, "expected 2 function_declaration matches: Main + Open")
	assert.GreaterOrEqual(t, out.FilesScanned, 2)
	for k := range out.ByFile {
		assert.False(t, filepath.IsAbs(k), "by_file key %q must be repo-relative", k)
	}
	assert.Equal(t, 1, out.ByFile["main.go"])
	assert.Equal(t, 1, out.ByFile["lib/lib.go"])
}

// TestAstIntegration_Explain pins the failure-mode cases (a)/(b) plus the
// happy path against a 5-line snippet.
func TestAstIntegration_Explain(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}

	t.Run("happy_path_5_line", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{
			"operation":"explain",
			"language":"go",
			"snippet":"package p\n\nfunc F() error {\n  return nil\n}\n"
		}`)
		require.False(t, isErr, "explain failed: %s", body)
		assert.Contains(t, body, "function_declaration")
		assert.Contains(t, body, "identifier")
	})

	t.Run("empty_snippet_errors", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"go","snippet":""}`)
		require.True(t, isErr)
		assert.Contains(t, body, "snippet")
	})

	t.Run("unsupported_language_errors", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"klingon","snippet":"x"}`)
		require.True(t, isErr)
		assert.Contains(t, body, "unsupported language")
	})
}

// TestAstIntegration_KindLeafFindsMethods exercises the v2 idiom that
// replaces the deleted search op: a wildcard pattern + kind leaf scoped to
// the method_declaration kind. Verifies hydration runs against
// NoOpBackend (EnclosingNodeID stays empty client-side).
func TestAstIntegration_KindLeafFindsMethods(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation":"match",
		"language":"go",
		"pattern":"$_",
		"where":{"kind":{"of":"$match","is":"method_declaration"}}
	}`)
	require.False(t, isErr, "match failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.NotEmpty(t, out.Matches, "expected at least one method_declaration match (Close on *T)")
	for _, m := range out.Matches {
		assert.Empty(t, m.EnclosingNodeID, "client-side hydration uses NoOpBackend; EnclosingNodeID must be empty")
	}
}

// TestAstIntegration_ListNodeKinds verifies non-empty Go output and error
// path for an unsupported language.
func TestAstIntegration_ListNodeKinds(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}

	t.Run("go_non_empty", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"list_node_kinds","language":"go"}`)
		require.False(t, isErr, "list_node_kinds failed: %s", body)
		var out struct {
			Language  string   `json:"language"`
			NodeKinds []string `json:"node_kinds"`
			Count     int      `json:"count"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, "go", out.Language)
		assert.Greater(t, out.Count, 50)
		assert.Contains(t, out.NodeKinds, "function_declaration")
		assert.Contains(t, out.NodeKinds, "method_declaration")
		assert.Contains(t, out.NodeKinds, "call_expression")
	})

	t.Run("unsupported_language_errors", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"list_node_kinds","language":"klingon"}`)
		require.True(t, isErr)
		assert.Contains(t, body, "unsupported language")
	})
}

// TestAstIntegration_EmptyResultHint verifies the LLM-facing guidance
// text surfaces when the query returns 0 matches.
func TestAstIntegration_EmptyResultHint(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation":"match",
		"language":"go",
		"pattern":"go func($$$ARGS) { $$$BODY }"
	}`)
	require.False(t, isErr, "expected zero-match result without error, got: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Empty(t, out.Matches, "pattern must produce zero matches")
	assert.NotEmpty(t, out.Hint, "Hint field must be populated when matches is empty")
	assert.Contains(t, out.Hint, "broader scope")
	assert.Contains(t, out.Hint, "simplify")
}
