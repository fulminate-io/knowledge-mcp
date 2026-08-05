// SPDX-License-Identifier: Apache-2.0

// ast_patterns_test.go — sibling-form alternation AS THE CALLER RECEIVES IT.
//
// Every assertion goes through callAst and json.Unmarshal, for the reason the
// replace wire tests were written the same way: a mistyped response key decodes
// to a zero value with no error, so only decoding under the real key proves the
// handler put pattern_errors on the wire at all.
//
// The two behaviors under test pull in opposite directions and both matter. One
// bad member must not destroy the good members' results; and a call where
// NOTHING could run must still fail loudly rather than return an empty success.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// altFixture carries two distinct call shapes, so two good alternation members
// each contribute exactly one match and the union total is checkable.
const altFixture = `package fix

func A() {
	alpha(one)
	beta(two)
}
`

// altBadParse is rejected by the DSL parser (bare $), so it fails before any
// grammar is consulted. altBadCompile parses cleanly and then fails to compile
// under every context wrapper — the two failure stages this step changed.
const (
	altBadParse   = "$"
	altBadCompile = "@@@"
)

// patternErrorShape decodes one pattern_errors entry. It is STRUCTURED on the
// wire, not a bare message: without the index a caller reading a partial result
// cannot tell which of their sibling forms went missing.
type patternErrorShape struct {
	Index   int    `json:"index"`
	Pattern string `json:"pattern"`
	Error   string `json:"error"`
}

// altReply decodes the fields these tests read across all three ops. total is
// absent from replace and pattern_errors is absent from a clean call; both
// decode to their zero value, which is exactly what the assertions distinguish.
type altReply struct {
	Total         int                 `json:"total"`
	FilesChanged  int                 `json:"files_changed"`
	PatternErrors []patternErrorShape `json:"pattern_errors"`
}

// altRepo writes the fixture plus a go.mod and returns (dir, sourcePath).
func altRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "fix.go")
	require.NoError(t, os.WriteFile(src, []byte(altFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir, src
}

// callAlt runs one ast call over a fresh fixture and returns the decoded reply
// plus the raw body, so a test can assert on either.
func callAlt(t *testing.T, dir string, args map[string]any) (altReply, string, bool) {
	t.Helper()
	args["language"] = "go"
	args["repo"] = dir
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	body, isErr, handled := callAst(t, astTestDeps{rootDir: dir}, string(encoded))
	require.True(t, handled)
	var out altReply
	if !isErr {
		require.NoError(t, json.Unmarshal([]byte(body), &out), "body: %s", body)
	}
	return out, body, isErr
}

// TestAstPatterns_PerPatternErrorsKeepUsableHalf pins the headline behavior: one
// member that cannot be used no longer costs the caller the members that can.
// The all-good control is what gives the partial totals meaning — the surviving
// members must return the SAME total they return when nothing failed, so a
// "partial success" that quietly dropped a good member too would still fail.
func TestAstPatterns_PerPatternErrorsKeepUsableHalf(t *testing.T) {
	const goodA = "alpha($$$X)"
	const goodB = "beta($$$X)"

	t.Run("control: both members good, no pattern_errors", func(t *testing.T) {
		dir, _ := altRepo(t)
		out, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "match", "patterns": []string{goodA, goodB},
		})
		require.False(t, isErr, "body: %s", body)
		assert.Equal(t, 2, out.Total, "the two good members match one call each")
		assert.Empty(t, out.PatternErrors, "a clean call reports no failures")
	})

	for name, bad := range map[string]string{
		"parse failure":   altBadParse,
		"compile failure": altBadCompile,
	} {
		t.Run(name+" keeps the other members", func(t *testing.T) {
			dir, _ := altRepo(t)
			out, body, isErr := callAlt(t, dir, map[string]any{
				"operation": "match", "patterns": []string{goodA, bad, goodB},
			})
			require.False(t, isErr, "one bad member must not destroy the call: %s", body)
			assert.Equal(t, 2, out.Total,
				"the good members return exactly what they return with no bad sibling present")

			require.Len(t, out.PatternErrors, 1, "and the failure is reported")
			got := out.PatternErrors[0]
			assert.Equal(t, 1, got.Index, "reported against the index the caller wrote")
			assert.Equal(t, bad, got.Pattern, "and echoing the offending source")
			assert.NotEmpty(t, got.Error)
		})
	}

	t.Run("the failure keeps its original wording", func(t *testing.T) {
		dir, _ := altRepo(t)
		out, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "match", "patterns": []string{goodA, altBadParse},
		})
		require.False(t, isErr, "body: %s", body)
		require.Len(t, out.PatternErrors, 1)
		assert.Contains(t, out.PatternErrors[0].Error, `patterns[1] "$"`,
			"the message format is unchanged; only its blast radius shrank")
	})

	t.Run("count reports pattern_errors too", func(t *testing.T) {
		dir, _ := altRepo(t)
		out, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "count", "patterns": []string{goodA, altBadCompile, goodB},
		})
		require.False(t, isErr, "body: %s", body)
		assert.Equal(t, 2, out.Total)
		require.Len(t, out.PatternErrors, 1)
		assert.Equal(t, 1, out.PatternErrors[0].Index)
	})

	t.Run("replace reports pattern_errors and still rewrites the usable half", func(t *testing.T) {
		// This is the path where a dropped member costs the most: a rewrite
		// driven by some of the sibling forms, reported as though all of them
		// ran, is a migration certified complete over part of its blast radius.
		dir, src := altRepo(t)
		out, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "replace", "patterns": []string{goodA, altBadCompile, goodB},
			"replacement": "gamma($$$X)", "dry_run": false,
		})
		require.False(t, isErr, "body: %s", body)
		require.Len(t, out.PatternErrors, 1, "the unusable member is disclosed")
		assert.Equal(t, 1, out.PatternErrors[0].Index)
		assert.Equal(t, 1, out.FilesChanged)

		onDisk, rerr := os.ReadFile(src) //nolint:gosec // fixture path built by the test
		require.NoError(t, rerr)
		assert.Contains(t, string(onDisk), "gamma(one)", "the first good member rewrote")
		assert.Contains(t, string(onDisk), "gamma(two)", "and so did the second")
	})

	t.Run("every member failing still errors", func(t *testing.T) {
		// The boundary that keeps accumulation from becoming its own silent
		// zero: a success carrying no results and a list of errors would be
		// exactly the shape this work exists to remove.
		dir, _ := altRepo(t)
		_, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "match", "patterns": []string{altBadParse, altBadCompile},
		})
		require.True(t, isErr, "a call where nothing could run must fail: %s", body)
		assert.Contains(t, body, "patterns[0]", "and name every member that failed")
		assert.Contains(t, body, "compile pattern", "including the one that failed later, at compile")
	})
}

// TestAstPatterns_SinglePatternStillHardErrors pins that accumulation did not
// leak into the one-pattern contract. There is no usable half to preserve when
// a caller sends a single pattern, so a bad one is still fatal — at either
// stage — and no partial result is ever returned in its place.
func TestAstPatterns_SinglePatternStillHardErrors(t *testing.T) {
	for name, bad := range map[string]string{
		"parse failure":   altBadParse,
		"compile failure": altBadCompile,
	} {
		t.Run(name+" is fatal", func(t *testing.T) {
			dir, _ := altRepo(t)
			_, body, isErr := callAlt(t, dir, map[string]any{
				"operation": "match", "pattern": bad,
			})
			require.True(t, isErr, "the singular form still hard-errors: %s", body)
			assert.NotContains(t, body, "pattern_errors",
				"a hard error returns no partial result to attach failures to")
		})
	}

	t.Run("known positive: a good single pattern still runs", func(t *testing.T) {
		// Without this the two legs above are satisfiable by a singular form
		// that rejects everything.
		dir, _ := altRepo(t)
		out, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "match", "pattern": "alpha($$$X)",
		})
		require.False(t, isErr, "body: %s", body)
		assert.Equal(t, 1, out.Total)
		assert.Empty(t, out.PatternErrors)
	})

	t.Run("a single-element patterns[] that fails is also fatal", func(t *testing.T) {
		// One member is one member however it was spelled: there is still no
		// usable half, so the array form does not become a way to turn a fatal
		// pattern into an empty success.
		dir, _ := altRepo(t)
		_, body, isErr := callAlt(t, dir, map[string]any{
			"operation": "match", "patterns": []string{altBadCompile},
		})
		require.True(t, isErr, "body: %s", body)
	})
}
