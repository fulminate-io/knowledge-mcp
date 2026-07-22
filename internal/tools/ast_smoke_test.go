// SPDX-License-Identifier: Apache-2.0

// ast_smoke_test.go — end-to-end smoke test for the Phase B' wiring. Drives
// the full client-side intercept (MCP args → DSL parse → engine compile →
// walker → where evaluator → result hydration) against a tiny in-memory
// fixture. Verifies three shapes:
//
//   - kind:function_declaration filters to top-level funcs (2 matches)
//   - kind:method_declaration filters to receiver methods (3 matches)
//   - absent where falls through to pure structural match (count > 0)
//
// Reuses the existing scaffolding (astTestDeps + callAst from ast_test.go).
// No httptest / grpctest — the intercept is in-process by design.
//
// Locked DSL idiom: `$_` (wildcard pattern) + a where-tree leaf that
// references the built-in `$match` capture gates the outer matched node
// without an explicit named placeholder. `$match` is the engine's
// implicit binding for the outermost matched target node, populated by
// matchTreeWithNodes on every successful match.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// astSmokeFixture writes a single Go file with 2 top-level functions and 3
// methods on *T. Returned directory is an isolated t.TempDir().
func astSmokeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const sample = `package sample

type T struct{}

func TopLevelOne() {}
func TopLevelTwo() error { return nil }

func (t *T) MethodOne()                     {}
func (t *T) MethodTwo(x int) string         { return "" }
func (t *T) MethodThree()                   {}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sample.go"), []byte(sample), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sample\n\ngo 1.21\n"), 0o600))
	return dir
}

// TestAstSmoke_KindFunctionDeclaration drives a where-tree filter that
// retains only the 2 top-level function_declaration nodes and excludes the
// 3 method_declaration nodes from the same file.
func TestAstSmoke_KindFunctionDeclaration(t *testing.T) {
	repoDir := astSmokeFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation": "match",
		"language":  "go",
		"pattern":   "$_",
		"where":     {"kind": {"of": "$match", "is": "function_declaration"}}
	}`)
	require.False(t, isErr, "smoke test failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Len(t, out.Matches, 2, "expected exactly 2 function_declaration matches (TopLevelOne + TopLevelTwo)")
	for _, m := range out.Matches {
		assert.Equal(t, "sample.go", m.FilePath, "every match must be repo-relative to sample.go")
	}
}

// TestAstSmoke_KindMethodDeclaration drives the inverse filter — retains
// only method_declaration nodes (3 of them) and excludes top-level funcs.
func TestAstSmoke_KindMethodDeclaration(t *testing.T) {
	repoDir := astSmokeFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation": "match",
		"language":  "go",
		"pattern":   "$_",
		"where":     {"kind": {"of": "$match", "is": "method_declaration"}}
	}`)
	require.False(t, isErr, "smoke test failed: %s", body)

	var out matchResultsShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Len(t, out.Matches, 3, "expected exactly 3 method_declaration matches (MethodOne, MethodTwo, MethodThree)")
}

// TestAstSmoke_NoWhereStructuralOnly is the regression guard for the
// nil-where path: when the user supplies no where field, ParseWhere returns
// nil and Match treats it as "no filter, pure structural match". Pattern
// $_ (wildcard) matches every node so the count is bounded only by limit
// (default 100). The exact count varies with grammar internals; assert > 0.
func TestAstSmoke_NoWhereStructuralOnly(t *testing.T) {
	repoDir := astSmokeFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	body, isErr, _ := callAst(t, deps, `{
		"operation": "count",
		"language":  "go",
		"pattern":   "$_"
	}`)
	require.False(t, isErr, "no-where structural smoke failed: %s", body)

	var out struct {
		Total        int            `json:"total"`
		ByFile       map[string]int `json:"by_file"`
		FilesScanned int            `json:"files_scanned"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Positive(t, out.Total, "structural-only walk should produce >0 matches across the fixture")
	assert.GreaterOrEqual(t, out.FilesScanned, 1)
}
