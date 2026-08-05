// SPDX-License-Identifier: Apache-2.0

// ast_limit_test.go — pins the meaning of the ast tool's `limit` argument:
// it bounds how many matches operation=match RENDERS and nothing else.
// count and replace traverse the full scope and
// ignore it entirely, and match reports the full-walk count in `total` even
// when it renders fewer results.
//
// Every subtest runs against a 150-call corpus — deliberately above the old
// engine default of 100 — so a truncating walk is visible as a wrong number
// rather than a coincidence.

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// astManyMatchCallSites is how many oldName call sites the fixture carries —
// deliberately above the old engine default of 100, so a truncating walk shows
// up as a wrong number rather than a coincidence. Every assertion below spells
// its expectation out as a literal instead of deriving it from this constant:
// a fixture and an expectation that move together cannot fail.
const astManyMatchCallSites = 150

// astManyMatchFixture writes corpus.go with astManyMatchCallSites single-line
// oldName call sites plus the one declaration (a function_declaration, so the
// oldName($X) pattern never counts it) and a go.mod into t.TempDir(),
// returning the directory. Mirrors astFixtureRepo's shape at corpus scale.
func astManyMatchFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	var b strings.Builder
	b.WriteString("package fix\n\nfunc oldName(x int) int { return x }\n\n")
	for i := range astManyMatchCallSites {
		b.WriteString(fmt.Sprintf("func F%03d() int { return oldName(%d) }\n", i, i))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "corpus.go"), []byte(b.String()), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir
}

func TestAstLimit_RenderBoundOnly(t *testing.T) {
	t.Run("count_ignores_limit", func(t *testing.T) {
		dir := astManyMatchFixture(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"count",
			"language":"go",
			"pattern":"oldName($X)",
			"limit":5
		}`)
		require.False(t, isErr, "count failed: %s", body)

		var out struct {
			Total  int            `json:"total"`
			ByFile map[string]int `json:"by_file"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 150, out.Total, "count must walk the whole scope regardless of limit")
		assert.Equal(t, 150, out.ByFile["corpus.go"], "by_file must carry the complete per-file total")
	})

	t.Run("match_total_is_full_walk", func(t *testing.T) {
		dir := astManyMatchFixture(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"match",
			"language":"go",
			"pattern":"oldName($X)",
			"limit":5
		}`)
		require.False(t, isErr, "match failed: %s", body)

		var out matchResultsShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Len(t, out.Matches, 5, "limit bounds how many matches are rendered")
		assert.Equal(t, 150, out.Total, "total must report the full-walk count, not the rendered count")
	})

	t.Run("match_default_render_100", func(t *testing.T) {
		dir := astManyMatchFixture(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"match",
			"language":"go",
			"pattern":"oldName($X)"
		}`)
		require.False(t, isErr, "match failed: %s", body)

		var out matchResultsShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Len(t, out.Matches, 100, "an absent limit renders the default bound")
		assert.Equal(t, 150, out.Total, "total must report the full-walk count under the default bound too")
	})

	t.Run("replace_dry_run_all_150", func(t *testing.T) {
		dir := astManyMatchFixture(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}
		before, rerr := os.ReadFile(filepath.Join(dir, "corpus.go"))
		require.NoError(t, rerr)

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"oldName($X)",
			"replacement":"newName($X)",
			"limit":5
		}`)
		require.False(t, isErr, "replace failed: %s", body)

		var out struct {
			MatchesReplaced int `json:"matches_replaced"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 150, out.MatchesReplaced, "replace must cover the whole scope regardless of limit")

		after, rerr := os.ReadFile(filepath.Join(dir, "corpus.go"))
		require.NoError(t, rerr)
		assert.Equal(t, string(before), string(after), "a dry run must leave the file byte-identical")
	})

	t.Run("replace_apply_all_150", func(t *testing.T) {
		dir := astManyMatchFixture(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"oldName($X)",
			"replacement":"newName($X)",
			"dry_run":false
		}`)
		require.False(t, isErr, "replace failed: %s", body)

		var out struct {
			MatchesReplaced int `json:"matches_replaced"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 150, out.MatchesReplaced, "an applied replace must cover the whole scope")

		src, rerr := os.ReadFile(filepath.Join(dir, "corpus.go"))
		require.NoError(t, rerr)
		assert.Equal(t, 150, strings.Count(string(src), "newName("), "every call site must be rewritten on disk")
		assert.Equal(t, 1, strings.Count(string(src), "oldName("), "only the declaration keeps the old name")
	})
}
