// SPDX-License-Identifier: Apache-2.0

// ast_disclosure_test.go — what a caller learns about the compile their
// pattern produced, on every op and including the ops that produced nothing.
//
// The disclosure exists for the ZERO case. A pattern that silently compiled to
// a construct the caller did not write returns a clean, plausible, empty
// result, and no amount of re-reading the pattern reveals why — which is the
// whole defect the union compile was built to end. So the empty-result leg here
// is load-bearing rather than a completeness formality: disclosure that appears
// only alongside matches cannot diagnose the case it was added for.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompiledKindDisclosure(t *testing.T) {
	t.Run("match_carries_compiled_kind", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Close()"}`)
		require.False(t, isErr, "match failed: %s", body)
		var out astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		require.Len(t, out.Matches, 2, "the fixture carries two defer sites")
		for i, m := range out.Matches {
			assert.Equalf(t, "defer_statement", m.CompiledKind, "match %d carries no compiled kind", i)
			assert.NotEmptyf(t, m.CompiledContexts, "match %d carries no compiled contexts", i)
		}
		require.Len(t, out.Compiled, 1)
		assert.Equal(t, "defer_statement", out.Compiled[0].RootKind)
	})

	t.Run("count_reports_by_kind", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{"operation":"count","language":"java","pattern":"$T $N = $V;"}`)
		require.False(t, isErr, "count failed: %s", body)
		var out struct {
			Total    int            `json:"total"`
			ByKind   map[string]int `json:"by_kind"`
			Compiled []struct {
				RootKind string   `json:"root_kind"`
				Contexts []string `json:"contexts"`
			} `json:"compiled"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 2, out.Total)
		// The split is the point: a bare total of 2 hides that this pattern
		// matched two DIFFERENT constructs, which is exactly what a caller
		// censusing one of them needs to see.
		assert.Equal(t, map[string]int{"field_declaration": 1, "local_variable_declaration": 1}, out.ByKind)
		assert.Len(t, out.Compiled, 2, "count discloses the variants behind its number")
	})

	t.Run("replace_reports_compiled", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{"operation":"replace","language":"go","pattern":"defer $X.Close()","replacement":"defer safeClose($X)","dry_run":true}`)
		require.False(t, isErr, "replace failed: %s", body)
		var out struct {
			MatchesReplaced int `json:"matches_replaced"`
			Compiled        []struct {
				RootKind string   `json:"root_kind"`
				Contexts []string `json:"contexts"`
			} `json:"compiled"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 2, out.MatchesReplaced)
		require.Len(t, out.Compiled, 1, "the write path discloses its compile too — it is where a wrong one costs most")
		assert.Equal(t, "defer_statement", out.Compiled[0].RootKind)
	})

	t.Run("narrowed_disclosure_reaches_the_tool", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// javascript `if ($C) { $$$B }` compiles to an if_statement AND a
		// method_definition whose name is the keyword `if`; the narrowing drops the
		// member reading. The ast-package gate cannot reach the tool RESPONSE (the
		// ast package cannot import tools), so this subtest is the caller-visible
		// half: the `narrowed` key must carry the dropped variant with a non-empty
		// reason on ALL THREE ops, while the surviving `compiled` entries carry none.
		const pat = `if ($C) { $$$B }`
		type variant struct {
			RootKind string `json:"root_kind"`
			Reason   string `json:"reason"`
		}
		assertNarrowed := func(t *testing.T, op, body string, compiled, narrowed []variant) {
			t.Helper()
			require.NotEmptyf(t, narrowed, "%s: the narrowed member reading must reach the tool response: %s", op, body)
			assert.NotEmptyf(t, narrowed[0].Reason, "%s: the narrowed entry must carry a reason", op)
			for i, cv := range compiled {
				assert.Emptyf(t, cv.Reason, "%s: surviving compiled entry %d must carry no reason", op, i)
			}
		}

		bodyM, isErr, _ := callAst(t, deps, `{"operation":"match","language":"javascript","pattern":"`+pat+`"}`)
		require.False(t, isErr, "match failed: %s", bodyM)
		var mOut struct {
			Compiled []variant `json:"compiled"`
			Narrowed []variant `json:"narrowed"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyM), &mOut))
		assertNarrowed(t, "match", bodyM, mOut.Compiled, mOut.Narrowed)

		bodyC, isErr, _ := callAst(t, deps, `{"operation":"count","language":"javascript","pattern":"`+pat+`"}`)
		require.False(t, isErr, "count failed: %s", bodyC)
		var cOut struct {
			Compiled []variant `json:"compiled"`
			Narrowed []variant `json:"narrowed"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyC), &cOut))
		assertNarrowed(t, "count", bodyC, cOut.Compiled, cOut.Narrowed)

		bodyR, isErr, _ := callAst(t, deps, `{"operation":"replace","language":"javascript","pattern":"`+pat+`","replacement":"`+pat+`","dry_run":true}`)
		require.False(t, isErr, "replace failed: %s", bodyR)
		var rOut struct {
			Compiled []variant `json:"compiled"`
			Narrowed []variant `json:"narrowed"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyR), &rOut))
		assertNarrowed(t, "replace", bodyR, rOut.Compiled, rOut.Narrowed)
	})

	t.Run("empty_result_still_discloses", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// A pattern that compiles cleanly and matches nothing. Files WERE
		// scanned, so this is the diagnosable-zero case rather than a wrong
		// walk root.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Flush()"}`)
		require.False(t, isErr, "match failed: %s", body)
		var out astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		require.Empty(t, out.Matches, "the premise of this case is a zero result")
		require.NotEmpty(t, out.Compiled,
			"a zero result is the case the disclosure exists for; without it the caller cannot see what their pattern became")
		assert.Equal(t, "defer_statement", out.Compiled[0].RootKind)
		assert.NotEmpty(t, out.Compiled[0].Contexts)
	})

	t.Run("alternation_concatenates_and_dedupes_the_disclosure", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// Each alternation member compiles independently, so the disclosure is
		// their concatenation — otherwise a caller cannot tell which member
		// contributed which kind. The repeated member is the dedupe control:
		// without it, a passing concatenation and a passing dedupe are the same
		// observation.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","patterns":["defer $X.Close()","$X.Close()","defer $X.Close()"]}`)
		require.False(t, isErr, "alternation match failed: %s", body)
		var out astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &out))

		kinds := make([]string, 0, len(out.Compiled))
		for _, cv := range out.Compiled {
			kinds = append(kinds, cv.RootKind)
		}
		assert.Equal(t, []string{"defer_statement", "call_expression"}, kinds,
			"both members are disclosed, in pattern order, and the repeat collapses on (root kind, contexts)")
	})

	t.Run("multi_context_stamp_lists_every_producing_context", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// tree-sitter-go's source_file accepts a bare statement, so this
		// fragment compiles ERROR-free under Go's DECL wrapper and, identically,
		// under its STMT wrapper. The dedupe collapses them into one variant,
		// and that variant must report BOTH: naming only the first would report
		// the registry's ordering as a property of the pattern, which is wrong
		// in most of the registered grammars rather than in a corner.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Close()"}`)
		require.False(t, isErr, "go match failed: %s", body)
		var goOut astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &goOut))
		require.Len(t, goOut.Compiled, 1, "both wrappers compile the identical tree, so the union holds one variant")
		assert.Equal(t, []string{"decl", "stmt"}, goOut.Compiled[0].Contexts,
			"the surviving variant records every context that produced it, in wrapper order")
		assert.Equal(t, []string{"decl", "stmt"}, goOut.Compiled[0].Wrappers)
		require.NotEmpty(t, goOut.Matches)
		assert.Equal(t, []string{"decl", "stmt"}, goOut.Matches[0].CompiledContexts,
			"the per-match stamp carries the same set the variant does")

		// The paired control. Java's two readings of one declaration compile to
		// DIFFERENT trees, so they must NOT merge: an implementation that
		// flattened every candidate into one over-broad stamp would satisfy the
		// go leg above and fail here.
		body, isErr, _ = callAst(t, deps, `{"operation":"match","language":"java","pattern":"$T $N = $V;"}`)
		require.False(t, isErr, "java match failed: %s", body)
		var javaOut astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &javaOut))
		require.Len(t, javaOut.Compiled, 2, "a field and a local are different trees and must stay separate variants")

		byKind := map[string][]string{}
		for _, cv := range javaOut.Compiled {
			byKind[cv.RootKind] = cv.Contexts
		}
		assert.Equal(t, []string{"member"}, byKind["field_declaration"],
			"only the class-body wrapper produces a field, so its stamp names exactly one context")
		assert.Equal(t, []string{"stmt", "decl"}, byKind["local_variable_declaration"],
			"java's stmt and top wrappers compile the local identically, so that stamp names both")
	})
}

// TestNoMatchHintNamesDiagnosableCauses pins the rewritten emptyResultHint
// against the causes a caller can act on. The retired text opened by blaming
// the walk scope — the least likely cause of a zero, and the one the result
// itself cannot help diagnose.
func TestNoMatchHintNamesDiagnosableCauses(t *testing.T) {
	dir := astContextFixtureRepo(t)
	deps := astTestDeps{rootDir: dir, rootDirSet: true}

	body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Flush()"}`)
	require.False(t, isErr, "match failed: %s", body)
	var out astMatchBody
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Empty(t, out.Matches, "the premise of this test is a scanned-but-no-match zero")
	require.NotEmpty(t, out.Hint)

	assert.Contains(t, out.Hint, "compiled",
		"the hint points at the field carrying the root kinds the pattern produced")
	assert.Contains(t, out.Hint, "contexts",
		"the contexts are a SET per variant, so the hint says contexts, not context")
	assert.Contains(t, out.Hint, `context:"decl"|"stmt"|"expr"|"member"`,
		"the pin is named with its actual vocabulary, so the remedy is copy-pasteable")
	assert.Contains(t, out.Hint, "package_prefixes / include_tests",
		"the scope filters stay named — they are a real cause, just not the first one")
	assert.Contains(t, out.Hint, `pattern:"$_"`,
		"the wildcard + kind-leaf probe survives the rewrite")

	// Asserted on a fragment rather than the retired sentence itself: an
	// absence gate greps the whole package for that literal, and a test
	// quoting it would keep the gate red forever.
	assert.NotContains(t, out.Hint, "broader scope",
		"the retired text blamed the scope for what is usually a miscompiled pattern")
	// The matched-pair discipline the walk-root hints already keep: this hint
	// must stay distinguishable from the zero-scan one, which owns "wrong root".
	assert.NotContains(t, out.Hint, "wrong root")
	assert.Contains(t, out.Hint, "no matches")
}
