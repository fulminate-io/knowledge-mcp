// SPDX-License-Identifier: Apache-2.0

// layout_census_test.go — the per-grammar pure-layout-token census.
//
// THE QUESTION. Some grammars surface an anonymous token that exists only to
// terminate a line. Go's is the reason this census exists: a multi-line block
// carries a bare newline child between the last statement and the closing
// brace, a one-line block does not, and a matcher that compares ALL children
// therefore reads the layout of the target as a constraint. Which grammars do
// that is a measurement, not folklore — a hand list over 21 grammars is exactly
// what this package refuses everywhere else.
//
// THE PROBE, made mechanical so it needs no judgment: parse the SAME construct
// written multi-line and written on one line, then compare the two trees. The
// step that commissioned this census framed the comparison as "the innermost
// block's child lists"; "innermost block" has no definition that survives
// braces, do/end, the offside rule and let/in at once, so the comparison here
// is over EVERY node's full child list, which is a strict superset of it. A
// grammar surfaces a layout token when the multi-line tree carries an anonymous
// childless token, whitespace-only in source text, that the one-line tree does
// not carry as often.
//
// WHITESPACE-ONLY IS NECESSARY BUT NOT SUFFICIENT, and this is the half a
// blanket "strip whitespace tokens" rule would get wrong. A token can be
// whitespace-only and still carry meaning — that is precisely Python's offside
// rule. So layout=yes ALSO requires the two spellings to parse to an identical
// pre-order sequence of named node kinds: the one-line form is the same tree
// minus that child, so the token distinguishes nothing a caller could have
// meant. A whitespace-only differential over two DIFFERENT structures is
// recorded as layout=no, naming the structural difference as the reason.
//
// A GRAMMAR THAT CANNOT BE PROBED gets an explicit reasoned SKIP, never a
// silent omission. Either spelling failing to parse, or parsing to a tree
// carrying ERROR nodes, is a skip: a garbage tree yields a garbage verdict, and
// a verdict is only worth recording when both trees are clean.
//
// THE ARTIFACT IS SELF-VERIFYING. This census is hermetic — every probe is an
// inline snippet and no corpus is walked — so the committed testdata file is
// compared against the fresh measurement on every run and a divergence is a
// failure naming the regeneration command. That is stronger than the corpus
// census's write-behind-an-env-var shape, which cannot compare because its
// input lives outside the repo. The env var still selects a write, so
// regenerating is a deliberate act and an ordinary suite run never dirties
// testdata.
//
// PERF SHAPE: 42 small parses, two per grammar, run once. Serial; there is no
// corpus walk and nothing to parallelize.

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// layoutCensusEnv names the environment variable that enables the artifact
// write. Unset means "measure and compare, write nothing".
const layoutCensusEnv = "AST_LAYOUT_CENSUS_WRITE"

// layoutCensusName is the committed artifact under testdata/.
const layoutCensusName = "layout_token_census.txt"

// layoutProbe is one grammar's differential: the same construct spelled across
// several lines and spelled on one. Nothing but the layout may differ between
// the two — a probe whose spellings also differ in content would measure the
// content difference and report it as layout.
type layoutProbe struct {
	lang       treesitter.Language
	multiLine  string
	singleLine string
}

// layoutProbes covers every registered grammar. Each pair is a body holding
// two statements (or, where a grammar has no such form, the nearest construct
// whose layout can vary) written both ways.
var layoutProbes = []layoutProbe{
	{lang: treesitter.LangBash,
		multiLine:  "f() {\n  a\n  b\n}\n",
		singleLine: "f() { a; b; }\n"},
	{lang: treesitter.LangC,
		multiLine:  "void f() {\n  a();\n  b();\n}\n",
		singleLine: "void f() { a(); b(); }\n"},
	{lang: treesitter.LangCPP,
		multiLine:  "void f() {\n  a();\n  b();\n}\n",
		singleLine: "void f() { a(); b(); }\n"},
	{lang: treesitter.LangCSharp,
		multiLine:  "class C {\n  void M() {\n    A();\n    B();\n  }\n}\n",
		singleLine: "class C { void M() { A(); B(); } }\n"},
	{lang: treesitter.LangElixir,
		multiLine:  "def f do\n  a()\n  b()\nend\n",
		singleLine: "def f do a(); b() end\n"},
	{lang: treesitter.LangElm,
		multiLine:  "f =\n    let\n        x = 1\n    in\n    x\n",
		singleLine: "f = let x = 1 in x\n"},
	{lang: treesitter.LangGo,
		multiLine:  "package p\n\nfunc f() {\n\ta()\n\tb()\n}\n",
		singleLine: "package p\n\nfunc f() { a(); b() }\n"},
	{lang: treesitter.LangGroovy,
		multiLine:  "def f() {\n  a()\n  b()\n}\n",
		singleLine: "def f() { a(); b() }\n"},
	{lang: treesitter.LangJava,
		multiLine:  "class C {\n  void m() {\n    a();\n    b();\n  }\n}\n",
		singleLine: "class C { void m() { a(); b(); } }\n"},
	{lang: treesitter.LangJavaScript,
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"},
	{lang: treesitter.LangKotlin,
		multiLine:  "fun f() {\n    a()\n    b()\n}\n",
		singleLine: "fun f() { a(); b() }\n"},
	{lang: treesitter.LangLua,
		multiLine:  "function f()\n  a()\n  b()\nend\n",
		singleLine: "function f() a() b() end\n"},
	{lang: treesitter.LangOCaml,
		multiLine:  "let f () =\n  a ();\n  b ()\n",
		singleLine: "let f () = a (); b ()\n"},
	{lang: treesitter.LangPython,
		multiLine:  "def f():\n    a()\n    b()\n",
		singleLine: "def f(): a(); b()\n"},
	{lang: treesitter.LangRuby,
		multiLine:  "def m\n  a\n  b\nend\n",
		singleLine: "def m; a; b; end\n"},
	{lang: treesitter.LangRust,
		multiLine:  "fn f() {\n    a();\n    b();\n}\n",
		singleLine: "fn f() { a(); b(); }\n"},
	{lang: treesitter.LangScala,
		multiLine:  "object O {\n  def m() = {\n    a()\n    b()\n  }\n}\n",
		singleLine: "object O { def m() = { a(); b() } }\n"},
	{lang: treesitter.LangSwift,
		multiLine:  "func f() {\n    a()\n    b()\n}\n",
		singleLine: "func f() { a(); b() }\n"},
	{lang: treesitter.LangTSX,
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"},
	{lang: treesitter.LangTypeScript,
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"},
}

// layoutVerdict is one grammar's measured result.
type layoutVerdict struct {
	lang   string
	layout bool
	token  string
	reason string
	skip   string
}

// line renders the verdict in the census format. layout=yes always carries the
// why= clause: an artifact stating a token is pure layout without saying how
// that was established is the hand list this census replaced.
func (v layoutVerdict) line() string {
	if v.skip != "" {
		return fmt.Sprintf("lang=%s SKIP %s", v.lang, v.skip)
	}
	if !v.layout {
		return fmt.Sprintf("lang=%s layout=no token=- why=%s", v.lang, v.reason)
	}
	return fmt.Sprintf("lang=%s layout=yes token=%q why=%s", v.lang, v.token, v.reason)
}

// TestLayoutTokenCensus measures every registered grammar, asserts the verdicts
// measured during planning still hold, and compares the committed artifact
// against the fresh measurement.
func TestLayoutTokenCensus(t *testing.T) {
	require.Len(t, layoutProbes, len(registeredLangs()),
		"every registered grammar needs a probe — an unprobed grammar is an unmeasured verdict")

	verdicts := make([]layoutVerdict, 0, len(layoutProbes))
	for _, probe := range layoutProbes {
		v := measureLayout(t, probe)
		t.Logf("%s", v.line())
		verdicts = append(verdicts, v)
	}

	assertSeededVerdicts(t, verdicts)
	compareCensusArtifact(t, verdicts)
}

// layoutSeeds are the verdicts measured by hand during planning. They are
// asserted rather than left in prose so a census that disagrees FAILS instead
// of silently replacing them.
var layoutSeeds = map[string]bool{
	"go":         true,
	"javascript": false,
	"python":     false,
	"ruby":       false,
	"bash":       false,
	"swift":      false,
	"kotlin":     false,
	"elixir":     false,
}

// assertSeededVerdicts compares each seeded grammar's measurement against the
// planning result.
func assertSeededVerdicts(t *testing.T, verdicts []layoutVerdict) {
	t.Helper()
	seen := map[string]bool{}
	for _, v := range verdicts {
		want, seeded := layoutSeeds[v.lang]
		if !seeded {
			continue
		}
		seen[v.lang] = true
		if v.skip != "" {
			t.Errorf("seeded grammar %s produced a SKIP (%s); a seed records a measured verdict and a skip is not one", v.lang, v.skip)
			continue
		}
		if v.layout != want {
			t.Errorf("seeded grammar %s measured layout=%v, planning measured layout=%v (%s).\n"+
				"  Do NOT edit the seed to agree with the measurement. Stop and report:\n"+
				"  either the probe is not the construct planning measured, or the grammar moved.",
				v.lang, v.layout, want, v.reason)
		}
	}
	for lang := range layoutSeeds {
		require.True(t, seen[lang], "seeded grammar %s produced no census row", lang)
	}
}

// compareCensusArtifact fails unless the committed artifact matches the fresh
// measurement, and writes it when layoutCensusEnv is set.
func compareCensusArtifact(t *testing.T, verdicts []layoutVerdict) {
	t.Helper()
	lines := make([]string, 0, len(verdicts))
	for _, v := range verdicts {
		lines = append(lines, v.line())
	}
	sort.Strings(lines)
	want := strings.Join(lines, "\n") + "\n"

	path := filepath.Join("testdata", layoutCensusName)
	if os.Getenv(layoutCensusEnv) != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
		t.Logf("census written: %s (%d rows)", path, len(lines))
		return
	}

	got, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	require.NoError(t, err, "census artifact missing — regenerate with %s=1", layoutCensusEnv)
	require.Equal(t, want, string(got),
		"census artifact is stale — regenerate with %s=1 go test ./cmd/knowledge/internal/ast/ -run TestLayoutTokenCensus", layoutCensusEnv)
}

// registeredLangs returns every language with a LangConfig, sorted.
func registeredLangs() []string {
	langRegistryMu.RLock()
	defer langRegistryMu.RUnlock()
	out := make([]string, 0, len(langRegistry))
	for lang := range langRegistry {
		out = append(out, string(lang))
	}
	sort.Strings(out)
	return out
}

// measureLayout runs one grammar's differential.
func measureLayout(t *testing.T, probe layoutProbe) layoutVerdict {
	t.Helper()
	v := layoutVerdict{lang: string(probe.lang)}

	multi, multiSrc, ok := parseClean(t, probe.lang, probe.multiLine)
	if !ok {
		v.skip = "the multi-line spelling does not parse cleanly under this grammar, so no differential can be taken"
		return v
	}
	defer multi.Close()
	single, singleSrc, ok := parseClean(t, probe.lang, probe.singleLine)
	if !ok {
		v.skip = "the one-line spelling does not parse cleanly under this grammar, so no differential can be taken"
		return v
	}
	defer single.Close()

	extra := extraWhitespaceTokens(
		anonWhitespaceTokens(multi.RootNode(), multiSrc),
		anonWhitespaceTokens(single.RootNode(), singleSrc),
	)
	if len(extra) == 0 {
		v.reason = "the multi-line spelling surfaces no anonymous whitespace-only child the one-line spelling lacks"
		return v
	}

	multiKinds := namedKinds(multi.RootNode())
	singleKinds := namedKinds(single.RootNode())
	if strings.Join(multiKinds, ",") != strings.Join(singleKinds, ",") {
		// Whitespace-only was necessary; this is the sufficiency test failing.
		v.reason = fmt.Sprintf(
			"a whitespace-only child %q is present only in the multi-line spelling, but the two spellings parse to DIFFERENT named structures (%d vs %d nodes), so the token cannot be ruled meaningless",
			extra[0], len(multiKinds), len(singleKinds))
		return v
	}
	v.layout = true
	v.token = extra[0]
	v.reason = fmt.Sprintf(
		"the one-line spelling parses to the same %d named nodes in the same order, minus this child, so the token distinguishes nothing a caller could have meant",
		len(multiKinds))
	return v
}

// parseClean parses src and reports ok=false when the parse fails or the tree
// carries ERROR nodes. The caller owns the returned tree.
func parseClean(t *testing.T, lang treesitter.Language, src string) (*sitter.Tree, []byte, bool) {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(src), lang)
	if err != nil {
		return nil, nil, false
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		tree.Close()
		return nil, nil, false
	}
	return tree, []byte(src), true
}

// anonWhitespaceTokens counts, across the whole tree, every anonymous childless
// token whose source text is whitespace only. Keyed by that text.
func anonWhitespaceTokens(root *sitter.Node, src []byte) map[string]int {
	out := map[string]int{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		count := int(n.ChildCount())
		for i := range count {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if !c.IsNamed() && c.ChildCount() == 0 {
				if text := c.Content(src); text != "" && strings.TrimSpace(text) == "" {
					out[text]++
				}
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// extraWhitespaceTokens returns, sorted, every token text the multi-line tree
// carries more often than the one-line tree.
func extraWhitespaceTokens(multi, single map[string]int) []string {
	out := make([]string, 0, len(multi))
	for text, n := range multi {
		if n > single[text] {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

// namedKinds returns the pre-order sequence of named node kinds in the tree.
// Two spellings that differ only in layout produce the same sequence; a
// difference means the layout change also changed what the source says.
func namedKinds(root *sitter.Node) []string {
	out := []string{}
	walkAll(root, func(n *sitter.Node) {
		if n != nil {
			out = append(out, n.Type())
		}
	})
	return out
}
