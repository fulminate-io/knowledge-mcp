// SPDX-License-Identifier: Apache-2.0

// ast_replace_test.go — handler-path coverage for ast operation:"replace".
// Drives the full client-side intercept (args → buildAstPatterns → matchAll →
// ApplyReplace → JSON) against the temp-dir fixtures from
// ast_integration_helpers_test.go / ast_test.go. Engine-level unit tests live
// in cmd/knowledge/internal/ast/replace_test.go +
// replace_apply_test.go.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replaceResultShape decodes the LLM-facing replace result wire shape.
type replaceResultShape struct {
	Applied         bool              `json:"applied"`
	DryRun          bool              `json:"dry_run"`
	FilesTouched    int               `json:"files_touched"`
	MatchesReplaced int               `json:"matches_replaced"`
	RefusedFiles    []string          `json:"refused_files"`
	RejectedFiles   []string          `json:"rejected_files"`
	Diffs           map[string]string `json:"diffs"`
}

// TestAstSchema_ReplaceOperation pins that the operation enum
// contains "replace" and the InputSchema advertises the replacement + dry_run
// properties.
func TestAstSchema_ReplaceOperation(t *testing.T) {
	def := AstToolDef()

	opProp, ok := def.InputSchema.Properties["operation"]
	require.True(t, ok, "operation property must exist")
	assert.Contains(t, opProp.Enum, "replace", "operation enum must advertise replace")

	_, hasReplacement := def.InputSchema.Properties["replacement"]
	assert.True(t, hasReplacement, "InputSchema must advertise the replacement property")

	_, hasDryRun := def.InputSchema.Properties["dry_run"]
	assert.True(t, hasDryRun, "InputSchema must advertise the dry_run property")

	assert.Contains(t, def.Description, "replace", "tool description must mention the replace op")
}

// TestAstReplace_DispatchAndValidation pins that dispatch
// routes operation:replace to handleAstReplace, a missing replacement errors
// naming replacement, and a dry-run on the fixture returns applied=false /
// dry_run=true / non-empty diffs with files unchanged.
func TestAstReplace_DispatchAndValidation(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	t.Run("missing_replacement_errors", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()"
		}`)
		require.True(t, isErr, "missing replacement must error")
		assert.Contains(t, body, "replacement")
	})

	t.Run("dry_run_previews_without_writing", func(t *testing.T) {
		before, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"safeClose($X)"
		}`)
		require.False(t, isErr, "dry-run replace failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.False(t, out.Applied, "absent dry_run defaults to a preview (applied=false)")
		assert.True(t, out.DryRun, "absent dry_run defaults to true")
		assert.NotEmpty(t, out.Diffs, "dry-run must populate diffs")
		assert.Contains(t, out.Diffs["main.go"], "safeClose(f)")

		after, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after), "dry-run must not write to disk")
	})
}

// TestAstReplace_DryRunPointerSemantics pins that an absent
// dry_run defaults to a dry run (no write); an explicit dry_run:false applies.
// Verifies the *flexBool pointer default-true semantics.
func TestAstReplace_DryRunPointerSemantics(t *testing.T) {
	t.Run("explicit_false_applies", func(t *testing.T) {
		repoDir := astIntegrationFixture(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"safeClose($X)",
			"dry_run":false
		}`)
		require.False(t, isErr, "apply replace failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.True(t, out.Applied, "dry_run:false must apply")
		assert.False(t, out.DryRun)
		assert.Equal(t, 1, out.FilesTouched)

		onDisk, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.Contains(t, string(onDisk), "safeClose(f)", "dry_run:false must rewrite the file")
	})

	t.Run("absent_defaults_to_dry_run", func(t *testing.T) {
		repoDir := astIntegrationFixture(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path
		before, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"safeClose($X)"
		}`)
		require.False(t, isErr)
		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.False(t, out.Applied)

		after, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after), "absent dry_run must not write")
	})
}

// TestAstReplace_EmptyReplacement_Deletes pins that an EXPLICIT empty
// replacement DELETES the matched ranges (the template interpolates to "",
// splicing nothing) — the deletion path the *string presence check unlocks.
// An ABSENT replacement still errors (covered by missing_replacement_errors).
func TestAstReplace_EmptyReplacement_Deletes(t *testing.T) {
	t.Run("explicit_empty_deletes_on_apply", func(t *testing.T) {
		repoDir := astIntegrationFixture(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path
		before, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		require.Contains(t, string(before), "defer f.Close()", "fixture must contain the target defer")

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":"",
			"dry_run":false
		}`)
		require.False(t, isErr, "empty-replacement delete failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.True(t, out.Applied, "dry_run:false must apply the deletion")
		assert.Equal(t, 1, out.FilesTouched)

		onDisk, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.NotContains(t, string(onDisk), "defer f.Close()", "the matched range must be deleted")
	})

	t.Run("explicit_empty_previews_under_default_dry_run", func(t *testing.T) {
		repoDir := astIntegrationFixture(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path
		before, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)

		body, isErr, _ := callAst(t, deps, `{
			"operation":"replace",
			"language":"go",
			"pattern":"defer $X.Close()",
			"replacement":""
		}`)
		require.False(t, isErr, "empty-replacement dry-run failed: %s", body)

		var out replaceResultShape
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.False(t, out.Applied, "absent dry_run defaults to preview")
		assert.True(t, out.DryRun)
		assert.NotEmpty(t, out.Diffs, "a deletion must still produce a preview diff")

		after, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after), "dry-run deletion must not write")
	})
}

// astReplaceNestFixture writes a single Go file whose only function body
// contains a nested call f(g()) so that sibling-form patterns f($$$_) and g()
// produce two matches whose byte ranges NEST inside one file.
func astReplaceNestFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const src = `package main

func run() {
	f(g())
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o600))
	return dir
}

// TestHandleAstReplace_OverlapRefused pins case (a): two
// sibling patterns whose matches nest in one file refuse that file whole and
// leave it unchanged.
func TestHandleAstReplace_OverlapRefused(t *testing.T) {
	repoDir := astReplaceNestFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path
	before, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
	require.NoError(t, err)

	// patterns f($$$_) (outer call) and g() (nested call) match overlapping
	// byte ranges in main.go. Apply mode to prove the file is NOT written.
	body, isErr, _ := callAst(t, deps, `{
		"operation":"replace",
		"language":"go",
		"patterns":["f($$$ARGS)","g()"],
		"replacement":"REPLACED",
		"dry_run":false
	}`)
	require.False(t, isErr, "replace failed: %s", body)

	var out replaceResultShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Contains(t, out.RefusedFiles, "main.go", "nested matches must refuse the file")
	assert.Equal(t, 0, out.FilesTouched, "a refused file is not touched")

	after, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "refused file must be unchanged")
}

// TestHandleAstReplace_LiteralDollarEscape pins case (b): a
// replacement containing $$ emits a single literal $ in the rewritten file.
func TestHandleAstReplace_LiteralDollarEscape(t *testing.T) {
	repoDir := astIntegrationFixture(t)
	deps := astTestDeps{rootDir: repoDir, rootDirSet: true} // explicit root: walk the fixture, not the guard path

	// $$ -> literal $ (the byte after the two dollars is a space, NOT a third
	// $, so it is the escape — not a $$$ sequence ref). $X -> the captured
	// receiver (f). The literal $ lands inside a string literal so the
	// rewrite stays valid Go and clears the re-parse gate. Expect the string
	// "$ from f".
	body, isErr, _ := callAst(t, deps, `{
		"operation":"replace",
		"language":"go",
		"pattern":"defer $X.Close()",
		"replacement":"println(\"$$ from $X\")",
		"dry_run":false
	}`)
	require.False(t, isErr, "replace failed: %s", body)

	var out replaceResultShape
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, 1, out.FilesTouched)

	onDisk, err := os.ReadFile(filepath.Join(repoDir, "main.go"))
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), `println("$ from f")`, "$$ must collapse to a single literal $")
}

// TestAstReplace_HelpDocumentsReplace pins that help("ast")
// documents the replace operation (template forms, dry_run default, overlap
// refuse-and-report, re-parse gate, worked example) and astToolDescription
// enumerates five ops including replace.
func TestAstReplace_HelpDocumentsReplace(t *testing.T) {
	doc, ok := helpTopics["ast"]
	require.True(t, ok, "help topic 'ast' must exist")

	// The dedicated operation:replace section.
	assert.Contains(t, doc, "operation:replace")
	// Template forms.
	assert.Contains(t, doc, "$NAME")
	assert.Contains(t, doc, "$$$NAME")
	assert.Contains(t, doc, "$$")
	// dry_run default + safety behaviors.
	assert.Contains(t, doc, "dry_run defaults TRUE")
	assert.Contains(t, doc, "refuse")
	assert.Contains(t, doc, "Re-parse gate")
	// Worked example.
	assert.Contains(t, doc, `"replacement": "safeClose($X)"`)

	// astToolDescription enumerates five ops including replace.
	assert.Contains(t, astToolDescription, "Five ops")
	assert.Contains(t, astToolDescription, "replace:")
}
