// SPDX-License-Identifier: Apache-2.0

// wrapper_acceptance_test.go — the union compile's acceptance gate against the
// fixture corpus.
//
// WHAT THIS TEST IS FOR, and why it is separate from every unit test in the
// package. The unit tests prove the compiler enumerates the right variants
// against hand-written fixtures. This one proves the resulting engine finds
// REAL sites in third-party source at scale — which is the only measurement
// that can tell a union that fixed the broken cases from a union that widened
// everything until something matched. Three of the four cases are patterns
// that returned NOTHING before the wrapper cascade landed; the fourth is the
// control that already worked and must not move.
//
// EVERY CASE IS A FLOOR PLUS A WAS-ZERO TRANSITION. The floors are deliberately
// slack against today's measurement because every number here is TREE-DERIVED
// from a third-party checkout that moves when the fixture is updated. The half
// that cannot drift is the transition: each positive case measured EXACTLY ZERO
// (or, for the TypeScript one, a hard compile error) against the pre-cascade
// engine, so any three-figure floor is a statement about the fix rather than
// about the fixture. Each case records both its pre-state and the shell command
// that re-derives its floor, so a future re-derivation edits one constant.
//
// COUNTS ARE COMMANDS, NEVER GREP HEADLINES. The headline occurrence counts are
// wrong answers here and are written down only to stop someone reinstating
// them: `Debug.Assert($X);` binds ONE argument, so it cannot match the
// two-argument or non-statement uses inside roslyn's 2387 occurrences of
// `Debug.Assert(`; `return $X;` cannot match a bare `return;`, so it cannot
// reach spring's 203 lines containing `return `. A gate asserting either
// headline would be RED against a correct engine.
//
// CORPUS DISCIPLINE, shared with corpus_identity_test.go: the fixture repos at
// ~/code/test-repos are walked CLIENT-SIDE, by absolute path, through
// ast.Match's repoDir argument only. They are never collected and never
// indexed, and this test never writes — it does not replace at all.
//
// PERF SHAPE: cs-roslyn's Compilers/Core/Portable is 913 files. The cases run
// as parallel subtests and nest no pool inside them, because ast.Match already
// fans out over NumCPU workers with a private parser and a privately recompiled
// pattern per worker — the package's standing tree-sitter discipline.

package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// The acceptance floors. Each is slack against the figure measured on
// 2026-08-04 through the live engine, and each sits far above the pre-cascade
// measurement of zero.
const (
	// C# `Debug.Assert($X);` over roslyn's Compilers/Core/Portable.
	// Pre-cascade: 0 matches across 913 files scanned — the pattern compiled
	// to a class-body field_declaration. Measured after: 2105.
	// Re-derive the floor with:
	//   cd ~/code/test-repos/cs-roslyn/src/Compilers/Core/Portable && \
	//     grep -rnE --include='*.cs' '^[[:space:]]*Debug\.Assert\([^,]*\);[[:space:]]*$' . | wc -l
	// → 2030 one-line SINGLE-ARGUMENT sites; the engine's answer exceeds it by
	// the multi-line single-argument sites grep cannot see on one line.
	floorCSharpDebugAssert = 2000

	// Java `return $X;` over spring's test-support.
	// Pre-cascade: 0 matches across 87 files scanned — the pattern compiled to
	// a field_declaration whose type leaf was the literal text "return".
	// Measured after: 168. Re-derive the floor with:
	//   cd ~/code/test-repos/java-spring/test-support && \
	//     grep -rnE --include='*.java' '^[[:space:]]*return [^;]+;[[:space:]]*$' . | wc -l
	// → 159 one-line sites. The engine's answer exceeds it by the multi-line
	// returns.
	floorJavaReturn = 150

	// TypeScript `private readonly $N: $T;` over vscode's base/common.
	// Pre-cascade: a HARD COMPILE ERROR ("pattern did not parse under any
	// context wrapper (tried decl,stmt,expr)") — no class member was
	// expressible at all, so the pre-state is not merely zero but unusable.
	// Measured after: 49, all rooted at public_field_definition under the
	// member wrapper with the trailing `;` absorbed. Re-derive with:
	//   cd ~/code/test-repos/ts-vscode/src/vs/base/common && \
	//     grep -rnE --include='*.ts' '^[[:space:]]*private readonly [A-Za-z_][A-Za-z0-9_]*: [^;=]+;[[:space:]]*$' . | wc -l
	// → 39 one-line members.
	floorTSClassMember = 30

	// THE NEGATIVE CONTROL. Java `throw new $E($$$A);` over spring's
	// test-support: 39 matches across 13 files, both before the cascade and
	// after. A throw statement cannot parse in a class body, so this pattern
	// already fell through to the statement wrapper correctly under first-wins
	// — which makes it the case that catches a union that "fixed" the others
	// by widening everything. Its floor is the measured figure itself, not a
	// slack one: this number is not supposed to move.
	floorJavaThrowControl = 39
)

// wantThrowKind is the compiled root kind the negative control must keep. A
// union that started matching this pattern out of some other construct would
// still satisfy the count floor, so the kind is asserted alongside it.
const wantThrowKind = "throw_statement"

// acceptanceCase is one corpus measurement: a pattern, where to run it, the
// floor it must clear, and — when the case pins one — the compiled root kind
// every match must carry.
type acceptanceCase struct {
	name     string
	lang     treesitter.Language
	repo     string
	prefix   string
	pattern  string
	minTotal int
	wantKind string
}

// acceptanceCases are the ticket's four ground truths. Three positives whose
// pre-cascade measurement was zero, and one control that was already correct.
//
// prefix carries a TRAILING SLASH wherever the sibling directory names could
// collide, matching corpus_identity_test.go: package-prefix filtering is a bare
// string-prefix test with no separator boundary. "test-support" is left bare
// because spring has no sibling directory sharing that prefix.
var acceptanceCases = []acceptanceCase{
	{
		name:     "csharp_debug_assert",
		lang:     treesitter.LangCSharp,
		repo:     "cs-roslyn",
		prefix:   "src/Compilers/Core/Portable/",
		pattern:  "Debug.Assert($X);",
		minTotal: floorCSharpDebugAssert,
	},
	{
		name:     "java_return",
		lang:     treesitter.LangJava,
		repo:     "java-spring",
		prefix:   "test-support",
		pattern:  "return $X;",
		minTotal: floorJavaReturn,
	},
	{
		name:     "ts_class_member",
		lang:     treesitter.LangTypeScript,
		repo:     "ts-vscode",
		prefix:   "src/vs/base/common/",
		pattern:  "private readonly $N: $T;",
		minTotal: floorTSClassMember,
	},
	{
		name:     "java_throw_negative_control",
		lang:     treesitter.LangJava,
		repo:     "java-spring",
		prefix:   "test-support",
		pattern:  "throw new $E($$$A);",
		minTotal: floorJavaThrowControl,
		wantKind: wantThrowKind,
	},
}

// TestWrapperAcceptanceCorpus runs each ground truth against the fixture corpus
// and asserts its floor — and, for the control, that its compiled root kind is
// unchanged.
func TestWrapperAcceptanceCorpus(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")
	if _, statErr := os.Stat(reposDir); os.IsNotExist(statErr) {
		t.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	for _, tc := range acceptanceCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runAcceptanceCase(t, reposDir, tc)
		})
	}
}

// runAcceptanceCase drives the real pipeline for one case: Parse, Compile with
// no context pin, Match. Nothing here writes.
func runAcceptanceCase(t *testing.T, reposDir string, tc acceptanceCase) {
	t.Helper()
	repoDir := filepath.Join(reposDir, tc.repo)
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("fixture repo %s is not present in the corpus", tc.repo)
	}

	pat, err := Parse(tc.pattern)
	require.NoError(t, err, "pattern %q must parse", tc.pattern)

	// An unpinned compile: the acceptance claim is about what the union does
	// for a caller who pinned nothing, which is how every one of these
	// patterns is written in practice.
	cp, err := Compile(pat, tc.lang, "")
	require.NoError(t, err, "pattern %q must compile under %s — the TypeScript case measured a hard compile error before the member wrapper was registered", tc.pattern, tc.lang)
	defer cp.Close()

	matches, stats, err := Match(context.Background(), repoDir, tc.lang, cp, nil, Scope{
		PackagePrefixes: []string{tc.prefix},
		IncludeTests:    true,
	})
	require.NoError(t, err)

	// files_scanned is asserted non-zero as the known-positive control on the
	// walk itself: a mistyped prefix admits no files, and every count below
	// would then be a truthful zero about nothing.
	require.NotZero(t, stats.FilesScanned, "the walk admitted no files — check the %q prefix", tc.prefix)

	require.GreaterOrEqualf(t, len(matches), tc.minTotal,
		"pattern %q over %s/%s returned %d matches, below the floor of %d "+
			"(measured pre-cascade: zero). Re-derive the floor from the command beside its constant before lowering it — "+
			"a miss here says the union does not reach real sites at scale",
		tc.pattern, tc.repo, tc.prefix, len(matches), tc.minTotal)

	if tc.wantKind == "" {
		return
	}
	for _, m := range matches {
		require.Equalf(t, tc.wantKind, m.CompiledKind,
			"the negative control changed the construct it matches out of: %s:%d compiled to %q, want %q",
			m.FilePath, m.StartLine, m.CompiledKind, tc.wantKind)
	}
}
