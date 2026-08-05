// SPDX-License-Identifier: Apache-2.0

// ast_lift_exclusions_param_test.go — the exclusion-override param as a CALLER
// reaches it, through InterceptAst rather than through the schema map.
//
// Grepping AstToolDef for the property name proves the string is present; it
// does not prove a call carrying that param survives the door. InterceptAst
// runs rejectUndeclaredParams against the declared property set before any
// handler sees the arguments, so a param that is threaded through astArgs but
// missing from the schema is rejected with an "unknown parameter" error and
// every downstream behavior becomes unreachable. This asserts the behavior.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liftFixtureRepo writes two Go files: one discovery always offers, and one
// under a pruned directory name that discovery declines unless the exclusions
// are lifted. Both carry the same call shape, so the pattern is not what
// distinguishes them — only the walk scope is.
//
// The directory is a plain temp dir rather than a git checkout, which puts
// discovery on its filesystem-walk path where the pruned-directory rule is the
// one that fires.
func liftFixtureRepo(t *testing.T) string {
	t.Helper()
	const src = `package fix

func A() {
	alpha(beta)
}
`
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.go"), []byte(src), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor", "dep"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "dep", "vend.go"), []byte(src), 0o600))
	return dir
}

// liftCountBody is the slice of the count response this test reads.
type liftCountBody struct {
	Total          int            `json:"total"`
	ExcludedByRule map[string]int `json:"excluded_by_rule"`
	DiscoveryPath  string         `json:"discovery_path"`
}

// TestAstSchema_ExclusionOverrideParamIsDeclaredAndAdmitted drives the param
// through the intercept three ways: a known-positive proving the rejection
// machinery is live on this tool, the admission itself, and the effect that
// distinguishes an admitted param from a merely tolerated one.
func TestAstSchema_ExclusionOverrideParamIsDeclaredAndAdmitted(t *testing.T) {
	// Known positive. Without this leg, "the response carries no unknown-param
	// error" is satisfied just as well by a tool that rejects nothing at all,
	// and the admission leg below would pass against a broken door.
	t.Run("the door really does reject an undeclared param", func(t *testing.T) {
		dir := liftFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		args, err := json.Marshal(map[string]any{
			"operation": "count", "language": "go", "pattern": "$F($$$A)",
			"repo": dir, "lift_exclusionz": true,
		})
		require.NoError(t, err)
		body, isErr, handled := callAst(t, deps, string(args))
		require.True(t, handled)
		assert.True(t, isErr, "a misspelled param must be refused, not ignored")
		assert.Contains(t, body, `unknown parameter "lift_exclusionz"`,
			"the refusal names the offending key so the caller can fix it")
	})

	t.Run("lift_exclusions is admitted", func(t *testing.T) {
		dir := liftFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		args, err := json.Marshal(map[string]any{
			"operation": "count", "language": "go", "pattern": "$F($$$A)",
			"repo": dir, "lift_exclusions": true,
		})
		require.NoError(t, err)
		body, isErr, handled := callAst(t, deps, string(args))
		require.True(t, handled)
		require.False(t, isErr, "lift_exclusions must reach the handler: %s", body)
		assert.NotContains(t, body, "unknown parameter",
			"a declared param must never be refused at the door")
	})

	// The effect leg. Admission alone cannot tell a param that is threaded into
	// the walk from one the schema declares and the handler drops: both answer
	// without an error. Two counts over the SAME tree with the same pattern,
	// differing only in the flag, can.
	t.Run("lifting widens the walk it was declared to widen", func(t *testing.T) {
		dir := liftFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		count := func(lift bool) liftCountBody {
			t.Helper()
			args, err := json.Marshal(map[string]any{
				"operation": "count", "language": "go", "pattern": "$F($$$A)",
				"repo": dir, "lift_exclusions": lift,
			})
			require.NoError(t, err)
			body, isErr, handled := callAst(t, deps, string(args))
			require.True(t, handled)
			require.False(t, isErr, "count failed: %s", body)
			var out liftCountBody
			require.NoError(t, json.Unmarshal([]byte(body), &out))
			return out
		}

		unlifted := count(false)
		lifted := count(true)

		// Neither side is a zero: the unlifted run finds the file discovery
		// offers, so a lifted total of 2 is a widening rather than the only
		// number this fixture can produce.
		assert.Equal(t, 1, unlifted.Total, "the unlifted walk sees only the un-pruned file")
		assert.Equal(t, 2, lifted.Total, "the lifted walk additionally sees the pruned one")
		assert.NotEmpty(t, unlifted.ExcludedByRule,
			"the unlifted run declined a file and must say under which rule")
		assert.Contains(t, lifted.DiscoveryPath, "lifted",
			"a lifted run stays distinguishable from a tree that had nothing to exclude")
	})
}
