// SPDX-License-Identifier: Apache-2.0

// python_offside_test.go — the offside-rule gate for the splice.
//
// WHY THIS TEST EXISTS SEPARATELY FROM THE RE-PARSE GATE. Under whole-span
// splicing the replacement de-indents the FIRST statement of a multi-statement
// Python body while statements 2..n keep the indentation they had in source.
// CPython rejects that with an IndentationError — but tree-sitter is
// ERROR-RECOVERING and parses it without producing an ERROR node, so
// RootNode().HasError() is false, the file never lands in RejectedFiles, and
// the corrupt bytes are written. applyEditsToSource's gate structurally cannot
// see this class of corruption. Source-anchored splicing should make it
// impossible; "should" is not a gate, so a real CPython compile is.
//
// BOTH ASSERTIONS ARE REQUIRED AND NEITHER IS REDUNDANT. py_compile catches
// corruption that no longer parses as Python. Byte-identity catches corruption
// that still COMPILES but moved statements between scopes — a body statement
// escaping to module scope is valid Python and a silent semantic change. A
// test keeping only py_compile would pass on exactly that.
//
// THE COMPILER GATE ITSELF IS CONTROLLED. A subtest de-indents one body
// statement by hand and requires py_compile to REJECT it, so a py_compile exit
// 0 elsewhere in this file is known to be discriminating rather than a
// subprocess that silently did nothing.
//
// A SINGLE-STATEMENT BODY IS NOT COVERAGE. The defect only appears from
// statement 2 onward, so every fixture body here holds at least two
// statements, and the set spans a flat body, a nested block, and functions
// indented two, four and eight spaces.

package ast

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// pythonOffsidePattern is deliberately indented TWO spaces while the fixtures
// are indented two, four and eight. Whitespace does not affect the parse, so
// the pattern matches all of them — and under whole-span splicing the
// template's own two-space indent is what lands on the first body statement,
// which is the corruption this file exists to catch.
const pythonOffsidePattern = "def $N():\n  $$$B"

// pythonOffsideFixture is the fixture tree: a flat multi-statement body, a
// nested block, and two bodies at different indentation widths.
func pythonOffsideFixture() map[string]string {
	return map[string]string{
		"multi.py": "def alpha():\n    first()\n    second()\n\n\n" +
			"def beta():\n    third()\n    fourth()\n",
		"nested.py": "def gamma():\n    if cond():\n        nested_one()\n" +
			"        nested_two()\n    tail()\n",
		"mixed.py": "def delta():\n  two_space_one()\n  two_space_two()\n\n\n" +
			"def epsilon():\n        eight_space_one()\n        eight_space_two()\n",
	}
}

// pythonOffsideMatches is the number of function definitions the pattern finds
// across the fixture tree. Asserted, so a run that quietly stopped matching
// cannot report a clean pass.
const pythonOffsideMatches = 5

// pyCompile runs CPython's py_compile over one file and returns its error.
// The combined output is logged so a failure names the offending line.
func pyCompile(t *testing.T, python, path string) error {
	t.Helper()
	out, err := exec.Command(python, "-m", "py_compile", path).CombinedOutput()
	if err != nil {
		t.Logf("py_compile %s: %v\n%s", filepath.Base(path), err, out)
	}
	return err
}

// compileAll runs py_compile over every fixture file in dir.
func compileAll(t *testing.T, python, dir string, files map[string]string) {
	t.Helper()
	for rel := range files {
		require.NoError(t, pyCompile(t, python, filepath.Join(dir, rel)),
			"%s must still compile after the rewrite", rel)
	}
}

func TestPythonOffsideRoundTrip(t *testing.T) {
	python, lookErr := exec.LookPath("python3")
	if lookErr != nil {
		t.Skip("python3 is not on PATH: the offside gate needs a real CPython compile, " +
			"and tree-sitter's error-recovering re-parse cannot stand in for it")
	}

	t.Run("py_compile_rejects_a_hand_deindented_body", func(t *testing.T) {
		// The control for every py_compile exit 0 below: prove the gate can
		// actually see the corruption it is here to catch.
		dir := t.TempDir()
		path := filepath.Join(dir, "broken.py")
		require.NoError(t, os.WriteFile(path,
			[]byte("def alpha():\n  first()\n    second()\n"), 0o600))
		require.Error(t, pyCompile(t, python, path),
			"a de-indented first statement must be rejected — otherwise py_compile proves nothing here")
	})

	t.Run("identity_template_is_byte_identical_and_compiles", func(t *testing.T) {
		files := pythonOffsideFixture()
		dir := fixtureRepo(t, files)
		compileAll(t, python, dir, files)

		res, matches := runSplice(t, dir, treesitter.LangPython,
			pythonOffsidePattern, pythonOffsidePattern, false)
		require.Equal(t, pythonOffsideMatches, matches,
			"match count is the known-positive control for a byte-identity assertion")
		require.Empty(t, res.RejectedFiles)
		require.Empty(t, res.RefusedFiles)

		for rel, want := range files {
			onDisk, err := os.ReadFile(filepath.Join(dir, rel))
			require.NoError(t, err)
			assert.Equal(t, want, string(onDisk),
				"identity template must leave %s byte-identical", rel)
		}
		compileAll(t, python, dir, files)
	})

	t.Run("one_token_rewrite_preserves_indentation_and_compiles", func(t *testing.T) {
		// The discriminating control: the file DOES change, every body byte
		// survives, and the result is still valid Python. A splice that
		// satisfied byte-identity by refusing to emit an edit fails here.
		files := pythonOffsideFixture()
		dir := fixtureRepo(t, files)

		res, matches := runSplice(t, dir, treesitter.LangPython,
			pythonOffsidePattern, "def renamed_$N():\n  $$$B", false)
		require.Equal(t, pythonOffsideMatches, matches)
		require.Empty(t, res.RejectedFiles)
		require.Equal(t, pythonOffsideMatches, res.MatchesReplaced)

		wantMulti := "def renamed_alpha():\n    first()\n    second()\n\n\n" +
			"def renamed_beta():\n    third()\n    fourth()\n"
		wantNested := "def renamed_gamma():\n    if cond():\n        nested_one()\n" +
			"        nested_two()\n    tail()\n"
		wantMixed := "def renamed_delta():\n  two_space_one()\n  two_space_two()\n\n\n" +
			"def renamed_epsilon():\n        eight_space_one()\n        eight_space_two()\n"

		for rel, want := range map[string]string{
			"multi.py": wantMulti, "nested.py": wantNested, "mixed.py": wantMixed,
		} {
			onDisk, err := os.ReadFile(filepath.Join(dir, rel))
			require.NoError(t, err)
			assert.Equal(t, want, string(onDisk),
				"only the def line may change in %s — every body byte comes from source", rel)
		}
		compileAll(t, python, dir, files)
	})
}
