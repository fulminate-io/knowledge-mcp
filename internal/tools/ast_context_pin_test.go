// SPDX-License-Identifier: Apache-2.0

// ast_context_pin_test.go — the `context` pin: what it narrows, and what each
// of its three failure paths tells the caller to pin instead.
//
// The pin's headline behavior is easy to get right and easy to test; its value
// is in the three failures, each of which turns a zero into a sentence naming
// the pin that would have worked. Each has its own subtest because they are
// three distinct causes with three distinct remedies, and a test that only
// exercised the narrowing would pass against an implementation that answered
// every bad pin with the same empty result.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// astContextFixtureRepo writes a Java class carrying the SAME declaration
// shape in both of its contexts — a class field and a method-body local — plus
// a Go file with two defers. The java pair is the ticket's opening case: one
// pattern text, two grammatical readings, and until the union compiled both, a
// caller could reach only whichever wrapper was registered first.
func astContextFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const javaSrc = `class Fix {
  int total = 1;

  void m() {
    int step = 2;
  }
}
`
	const goSrc = `package fix

import "os"

func A() {
	f, _ := os.Open("a")
	defer f.Close()
}

func B() {
	g, _ := os.Open("b")
	defer g.Close()
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Fix.java"), []byte(javaSrc), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.go"), []byte(goSrc), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir
}

// astMatchBody is the subset of a match reply these tests read.
type astMatchBody struct {
	Matches []struct {
		Captures map[string]struct {
			Text string `json:"text"`
		} `json:"captures"`
		CompiledKind     string   `json:"compiled_kind"`
		CompiledContexts []string `json:"compiled_contexts"`
	} `json:"matches"`
	Total    int    `json:"total"`
	Hint     string `json:"hint"`
	Compiled []struct {
		Contexts []string `json:"contexts"`
		Wrappers []string `json:"wrappers"`
		RootKind string   `json:"root_kind"`
		Absorbed []string `json:"absorbed"`
	} `json:"compiled"`
}

// capturedNames returns the text bound to capture name across every match, so
// a narrowing assertion can name WHICH declaration survived rather than only
// how many did.
func (b astMatchBody) capturedNames(name string) []string {
	out := make([]string, 0, len(b.Matches))
	for _, m := range b.Matches {
		out = append(out, m.Captures[name].Text)
	}
	return out
}

func TestAstContextPin(t *testing.T) {
	t.Run("pin_narrows_union", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// The known positive. Without it, both pinned runs returning one match
		// each would be indistinguishable from a pin that broke matching
		// outright and a fixture that only ever had one site.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"java","pattern":"$T $N = $V;"}`)
		require.False(t, isErr, "unpinned match failed: %s", body)
		var unpinned astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &unpinned))
		assert.ElementsMatch(t, []string{"total", "step"}, unpinned.capturedNames("N"),
			"unpinned, this pattern is grammatical as a field AND as a local, and must find both")

		body, isErr, _ = callAst(t, deps, `{"operation":"match","language":"java","pattern":"$T $N = $V;","context":"member"}`)
		require.False(t, isErr, "member-pinned match failed: %s", body)
		var member astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &member))
		assert.Equal(t, []string{"total"}, member.capturedNames("N"),
			"the member pin keeps the class field and drops the method-body local")
		require.NotEmpty(t, member.Compiled)
		assert.Len(t, member.Compiled, 1, "the pin narrows the compiled set too, not just the results")
		assert.Equal(t, "field_declaration", member.Compiled[0].RootKind)

		body, isErr, _ = callAst(t, deps, `{"operation":"match","language":"java","pattern":"$T $N = $V;","context":"stmt"}`)
		require.False(t, isErr, "stmt-pinned match failed: %s", body)
		var stmt astMatchBody
		require.NoError(t, json.Unmarshal([]byte(body), &stmt))
		assert.Equal(t, []string{"step"}, stmt.capturedNames("N"),
			"the stmt pin keeps the local — and selects it by SET MEMBERSHIP: its variant's contexts are [stmt decl]")
		require.Len(t, stmt.Compiled, 1)
		assert.Equal(t, "local_variable_declaration", stmt.Compiled[0].RootKind)
		assert.Contains(t, stmt.Compiled[0].Contexts, "stmt",
			"an equality test against the variant's first context would have selected nothing here")

		// The pin narrows count and replace identically. A caller who censused
		// with a pin and then rewrote with the same pin must get the same set
		// both times; there is no reading under which the write path should be
		// wider than the read path that justified it.
		body, isErr, _ = callAst(t, deps, `{"operation":"count","language":"java","pattern":"$T $N = $V;","context":"member"}`)
		require.False(t, isErr, "member-pinned count failed: %s", body)
		var counted struct {
			Total  int            `json:"total"`
			ByKind map[string]int `json:"by_kind"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &counted))
		assert.Equal(t, 1, counted.Total, "count honors the same pin match did")
		assert.Equal(t, map[string]int{"field_declaration": 1}, counted.ByKind)

		body, isErr, _ = callAst(t, deps, `{"operation":"replace","language":"java","pattern":"$T $N = $V;","replacement":"$T $N = $V;","context":"member","dry_run":true}`)
		require.False(t, isErr, "member-pinned replace failed: %s", body)
		var replaced struct {
			MatchesReplaced int `json:"matches_replaced"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &replaced))
		assert.Equal(t, 1, replaced.MatchesReplaced,
			"replace rewrites the pinned reading only — the unpinned union would have touched the local too")
	})

	t.Run("unknown_value_names_the_four", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Close()","context":"block"}`)
		require.True(t, isErr, "a context outside the vocabulary must fail loud, got: %s", body)
		for _, want := range []string{"decl", "stmt", "expr", "member"} {
			assert.Containsf(t, body, want, "the rejection must name %q as an available context", want)
		}
	})

	t.Run("unregistered_context_names_available", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// member is a real context; go simply registers no wrapper for it —
		// Go has no class body, so a struct field is reachable only as part of
		// the type declaration that owns it. The remedy is the list of contexts
		// go DOES register, read from the live registry rather than restated
		// here.
		//
		// This case was written against java + expr, which stopped exercising
		// the path the moment java gained an expression wrapper: the pin became
		// valid and the failure moved to a parse error further down. It needs a
		// pair the registry genuinely lacks, and go/member is that pair.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"$N $T","context":"member"}`)
		require.True(t, isErr, "a context this language registers no wrapper for must fail loud, got: %s", body)
		assert.Contains(t, body, "registers no")
		for _, want := range []string{"decl", "stmt", "expr"} {
			assert.Containsf(t, body, want, "the rejection must name %q, which go does register", want)
		}
	})

	t.Run("no_candidate_names_hosting_contexts", func(t *testing.T) {
		dir := astContextFixtureRepo(t)
		deps := astTestDeps{rootDir: dir, rootDirSet: true}

		// go registers an expression wrapper, so this pin passes both earlier
		// gates — and no wrapper HOSTS a defer statement as an expression. This
		// is the path that turns a wrong-context zero into a sentence: it names
		// the contexts that did produce a candidate.
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"defer $X.Close()","context":"expr"}`)
		require.True(t, isErr, "a pin no variant carries must fail loud rather than return zero, got: %s", body)
		assert.Contains(t, body, "this pattern compiles in decl,stmt",
			"the caller is told which pin would have worked")
		assert.Contains(t, body, `excluded by context pin "expr"`,
			"an excluded candidate says it was excluded, not that it failed to parse")
	})
}
