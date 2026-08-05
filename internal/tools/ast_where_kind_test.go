// SPDX-License-Identifier: Apache-2.0

// ast_where_kind_test.go — the where-tree kind refusal as the TOOL serves it:
// present on all three walking ops, and reading the same vocabulary
// operation=list_node_kinds prints.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// whereKindFixture is one call expression, so the pattern below matches and a
// zero result can only come from the filter.
const whereKindFixture = `package fix

func A() {
	alpha(beta)
}
`

// whereKindRepo writes the fixture and returns (dir, sourcePath) so a replace
// leg can prove whether the file on disk moved.
func whereKindRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "fix.go")
	require.NoError(t, os.WriteFile(src, []byte(whereKindFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir, src
}

// TestAstWhereKind_ValidatedOnMatchCountAndReplace pins the refusal on every op
// that walks. Replace is the one that matters most and it is the one with teeth
// here: it runs with dry_run FALSE, so the valid-kind control really rewrites
// the file and the bogus-kind leg really would have, had it not been refused.
func TestAstWhereKind_ValidatedOnMatchCountAndReplace(t *testing.T) {
	const bogusWhere = `{"kind":{"of":"F","is":"identifierr"}}`
	const validWhere = `{"kind":{"of":"F","is":"identifier"}}`

	for _, op := range []string{"match", "count"} {
		t.Run(op+"/refuses the bogus kind", func(t *testing.T) {
			dir, _ := whereKindRepo(t)
			deps := astTestDeps{rootDir: dir}
			args, err := json.Marshal(map[string]any{
				"operation": op, "language": "go", "pattern": "$F($$$A)",
				"repo": dir, "where": json.RawMessage(bogusWhere),
			})
			require.NoError(t, err)
			body, isErr, handled := callAst(t, deps, string(args))
			require.True(t, handled)
			require.True(t, isErr, "an unknown kind must be refused, not walked: %s", body)
			assert.Contains(t, body, "identifierr")
			assert.Contains(t, body, "did you mean")
		})

		t.Run(op+"/known positive: the valid kind still runs", func(t *testing.T) {
			// Without this leg the refusal above is satisfiable by an op that
			// errors on every where-tree it is handed.
			dir, _ := whereKindRepo(t)
			deps := astTestDeps{rootDir: dir}
			args, err := json.Marshal(map[string]any{
				"operation": op, "language": "go", "pattern": "$F($$$A)",
				"repo": dir, "where": json.RawMessage(validWhere),
			})
			require.NoError(t, err)
			body, isErr, handled := callAst(t, deps, string(args))
			require.True(t, handled)
			require.False(t, isErr, "a valid kind is not refused: %s", body)
			var out struct {
				Total int `json:"total"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &out))
			assert.Equal(t, 1, out.Total, "and it finds the one call in the fixture")
		})
	}

	t.Run("replace/refuses before writing anything", func(t *testing.T) {
		dir, src := whereKindRepo(t)
		deps := astTestDeps{rootDir: dir}
		args, err := json.Marshal(map[string]any{
			"operation": "replace", "language": "go", "pattern": "$F($$$A)",
			"replacement": "gamma($$$A)", "dry_run": false,
			"repo": dir, "where": json.RawMessage(bogusWhere),
		})
		require.NoError(t, err)
		body, isErr, handled := callAst(t, deps, string(args))
		require.True(t, handled)
		require.True(t, isErr, "a replace whose filter can never match must be refused: %s", body)
		assert.Contains(t, body, "identifierr")

		after, rerr := os.ReadFile(src) //nolint:gosec // fixture path built by the test
		require.NoError(t, rerr)
		assert.Equal(t, whereKindFixture, string(after),
			"and refused means nothing was written")
	})

	t.Run("replace/known positive: the same call with a valid kind does write", func(t *testing.T) {
		// This is what gives the leg above its meaning. Identical call but for
		// the kind name, and it rewrites the file — so the untouched file
		// above is the refusal's doing, not a replace that never worked.
		dir, src := whereKindRepo(t)
		deps := astTestDeps{rootDir: dir}
		args, err := json.Marshal(map[string]any{
			"operation": "replace", "language": "go", "pattern": "$F($$$A)",
			"replacement": "gamma($$$A)", "dry_run": false,
			"repo": dir, "where": json.RawMessage(validWhere),
		})
		require.NoError(t, err)
		body, isErr, handled := callAst(t, deps, string(args))
		require.True(t, handled)
		require.False(t, isErr, "replace failed: %s", body)

		after, rerr := os.ReadFile(src) //nolint:gosec // fixture path built by the test
		require.NoError(t, rerr)
		assert.Contains(t, string(after), "gamma(beta)")
		assert.NotEqual(t, whereKindFixture, string(after))
	})
}

// TestAstListNodeKinds_UsesSharedVocabulary is the behavioral half of the
// single-vocabulary guarantee: what the op PRINTS is what the validator READS.
// The ast package pins the validator's side of it; only here, where the handler
// lives, can the two be compared directly.
func TestAstListNodeKinds_UsesSharedVocabulary(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"list_node_kinds","language":"go"}`)
	require.False(t, isErr, "list_node_kinds failed: %s", body)

	var out struct {
		NodeKinds []string `json:"node_kinds"`
		Count     int      `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))

	shared, ok := ast.NodeKinds(treesitter.LangGo)
	require.True(t, ok)
	require.Greater(t, len(shared), 50, "setup: two empty lists would match vacuously")
	assert.Equal(t, shared, out.NodeKinds,
		"the op prints the enumeration the kind validator reads, not a second one")
	assert.Equal(t, len(shared), out.Count)
}
