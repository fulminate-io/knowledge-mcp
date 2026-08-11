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
// THE SECOND DIFFERENTIAL: TOKEN-SPAN ABSORPTION, reported in its own absorbed=
// clause and measured independently of the layout verdict. A grammar may put
// inter-child layout whitespace INSIDE the following node's leading anonymous
// token rather than surfacing it as a child of its own — the JSX grammars do,
// where a `<` spans "\n<". The whitespace-only detector above provably cannot
// see it: "\n<" is not whitespace-only, so that detector returns empty over a
// JSX probe pair. The two are separate dimensions of one question — does this
// grammar's layout leak into what the matcher compares — and a grammar can
// exhibit either, both or neither. layout= drives the child-list skip;
// absorbed= drives the trimmed token comparison.
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
// PERF SHAPE: two small parses per probed construct — one per grammar, plus a
// second for each of the two that accept JSX — run once. Serial; there is no
// corpus walk and nothing to parallelize.

package ast

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// layoutCensusEnv names the environment variable that enables the artifact
// write. Unset means "measure and compare, write nothing".
const layoutCensusEnv = "AST_LAYOUT_CENSUS_WRITE"

// layoutCensusName is the committed artifact under testdata/.
const layoutCensusName = "layout_token_census.txt"

// layoutPair is one construct's differential: the same code spelled across
// several lines and spelled on one. Nothing but the layout may differ between
// the two — a pair whose spellings also differ in content would measure the
// content difference and report it as layout.
type layoutPair struct {
	multiLine  string
	singleLine string
}

// layoutProbe is one grammar's probe set. Every registered grammar has exactly
// one probe — that is the coverage invariant — but a grammar may need MORE THAN
// ONE CONSTRUCT probed, because the two phenomena this census measures do not
// surface in the same syntax. A block body exposes a whitespace-only layout
// child; only JSX exposes whitespace absorbed INTO a token.
//
// THE FIRST PAIR IS THE CANONICAL LAYOUT PROBE. The layout verdict is the first
// pair that measures layout=yes, or the first pair's verdict when none does, so
// a single-pair grammar's row is exactly what it was before probe sets existed.
// Absorption is reported if ANY pair exhibits it, since a grammar that absorbs
// in one construct absorbs.
type layoutProbe struct {
	lang  treesitter.Language
	pairs []layoutPair
}

// layoutProbes covers every registered grammar. The canonical pair is a body
// holding two statements (or, where a grammar has no such form, the nearest
// construct whose layout can vary) written both ways.
var layoutProbes = []layoutProbe{
	{lang: treesitter.LangBash, pairs: []layoutPair{{
		multiLine:  "f() {\n  a\n  b\n}\n",
		singleLine: "f() { a; b; }\n"}}},
	{lang: treesitter.LangC, pairs: []layoutPair{{
		multiLine:  "void f() {\n  a();\n  b();\n}\n",
		singleLine: "void f() { a(); b(); }\n"}}},
	{lang: treesitter.LangCPP, pairs: []layoutPair{{
		multiLine:  "void f() {\n  a();\n  b();\n}\n",
		singleLine: "void f() { a(); b(); }\n"}}},
	{lang: treesitter.LangCSharp, pairs: []layoutPair{{
		multiLine:  "class C {\n  void M() {\n    A();\n    B();\n  }\n}\n",
		singleLine: "class C { void M() { A(); B(); } }\n"}}},
	{lang: treesitter.LangElixir, pairs: []layoutPair{{
		multiLine:  "def f do\n  a()\n  b()\nend\n",
		singleLine: "def f do a(); b() end\n"}}},
	{lang: treesitter.LangElm, pairs: []layoutPair{{
		multiLine:  "f =\n    let\n        x = 1\n    in\n    x\n",
		singleLine: "f = let x = 1 in x\n"}}},
	{lang: treesitter.LangGo, pairs: []layoutPair{{
		multiLine:  "package p\n\nfunc f() {\n\ta()\n\tb()\n}\n",
		singleLine: "package p\n\nfunc f() { a(); b() }\n"}}},
	{lang: treesitter.LangGroovy, pairs: []layoutPair{{
		multiLine:  "def f() {\n  a()\n  b()\n}\n",
		singleLine: "def f() { a(); b() }\n"}}},
	{lang: treesitter.LangJava, pairs: []layoutPair{{
		multiLine:  "class C {\n  void m() {\n    a();\n    b();\n  }\n}\n",
		singleLine: "class C { void m() { a(); b(); } }\n"}}},
	// javascript and tsx carry a SECOND construct: the block pair above cannot
	// exhibit token-span absorption, which only JSX child position produces.
	// The block pair stays first and keeps owning the layout verdict, so the
	// seeded javascript layout=no result is measured by exactly what measured it
	// before.
	{lang: treesitter.LangJavaScript, pairs: []layoutPair{{
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"}, jsxLayoutPair}},
	{lang: treesitter.LangKotlin, pairs: []layoutPair{{
		multiLine:  "fun f() {\n    a()\n    b()\n}\n",
		singleLine: "fun f() { a(); b() }\n"}}},
	{lang: treesitter.LangLua, pairs: []layoutPair{{
		multiLine:  "function f()\n  a()\n  b()\nend\n",
		singleLine: "function f() a() b() end\n"}}},
	{lang: treesitter.LangOCaml, pairs: []layoutPair{{
		multiLine:  "let f () =\n  a ();\n  b ()\n",
		singleLine: "let f () = a (); b ()\n"}}},
	{lang: treesitter.LangPython, pairs: []layoutPair{{
		multiLine:  "def f():\n    a()\n    b()\n",
		singleLine: "def f(): a(); b()\n"}}},
	{lang: treesitter.LangRuby, pairs: []layoutPair{{
		multiLine:  "def m\n  a\n  b\nend\n",
		singleLine: "def m; a; b; end\n"}}},
	{lang: treesitter.LangRust, pairs: []layoutPair{{
		multiLine:  "fn f() {\n    a();\n    b();\n}\n",
		singleLine: "fn f() { a(); b(); }\n"}}},
	{lang: treesitter.LangScala, pairs: []layoutPair{{
		multiLine:  "object O {\n  def m() = {\n    a()\n    b()\n  }\n}\n",
		singleLine: "object O { def m() = { a(); b() } }\n"}}},
	{lang: treesitter.LangSwift, pairs: []layoutPair{{
		multiLine:  "func f() {\n    a()\n    b()\n}\n",
		singleLine: "func f() { a(); b() }\n"}}},
	{lang: treesitter.LangTSX, pairs: []layoutPair{{
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"}, jsxLayoutPair}},
	{lang: treesitter.LangTypeScript, pairs: []layoutPair{{
		multiLine:  "function f() {\n  a();\n  b();\n}\n",
		singleLine: "function f() { a(); b(); }\n"}}},
}

// jsxLayoutPair is the JSX child-position differential, shared by the two
// grammars that accept JSX. It is byte-for-byte the pair the reproduction and
// the mechanism artifact use (jsx_layout_test.go), so a reader comparing the
// three is never comparing two different fixtures for one claim.
var jsxLayoutPair = layoutPair{
	multiLine:  jsxNewlineChild,
	singleLine: jsxNoWhitespaceAtAll,
}

// layoutVerdict is one grammar's measured result: the layout-child differential
// (layout/token) and, independently, the token-span-absorption differential
// (absorbed/absorbedToken). The two are SEPARATE dimensions of the same
// question — does this grammar's layout leak into the child list the matcher
// compares — and a grammar can exhibit either, both or neither.
type layoutVerdict struct {
	lang          string
	layout        bool
	token         string
	reason        string
	skip          string
	absorbed      bool
	absorbedToken string
}

// line renders the verdict in the census format. layout=yes always carries the
// why= clause: an artifact stating a token is pure layout without saying how
// that was established is the hand list this census replaced. The absorbed=
// clause is appended LAST so every row keeps its existing shape and gains
// exactly one trailing clause.
func (v layoutVerdict) line() string {
	if v.skip != "" {
		return fmt.Sprintf("lang=%s SKIP %s", v.lang, v.skip)
	}
	head := fmt.Sprintf("lang=%s layout=no token=- why=%s", v.lang, v.reason)
	if v.layout {
		head = fmt.Sprintf("lang=%s layout=yes token=%q why=%s", v.lang, v.token, v.reason)
	}
	if v.absorbed {
		return head + fmt.Sprintf(" absorbed=yes token=%q", v.absorbedToken)
	}
	return head + " absorbed=no"
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

// measureLayout runs every pair in one grammar's probe set and reduces them to
// a single row. The LAYOUT verdict is the first pair measuring layout=yes, or
// the first pair's verdict when none does — so a one-pair grammar's row is
// identical to what it was before probe sets existed. ABSORPTION is reported
// when ANY pair exhibits it: a grammar that absorbs in one construct absorbs,
// and the constructs that can exhibit it are not the ones that expose a layout
// child.
func measureLayout(t *testing.T, probe layoutProbe) layoutVerdict {
	t.Helper()
	require.NotEmpty(t, probe.pairs,
		"%s has an empty probe set — coverage counts probes, so an empty one is an unmeasured grammar that still satisfies the count", probe.lang)

	var out layoutVerdict
	for i, pair := range probe.pairs {
		v := measureLayoutPair(t, probe.lang, pair)
		if i == 0 || (!out.layout && v.layout) {
			absorbed, token := out.absorbed, out.absorbedToken
			out = v
			if absorbed {
				out.absorbed, out.absorbedToken = absorbed, token
			}
		}
		if v.absorbed && !out.absorbed {
			out.absorbed, out.absorbedToken = true, v.absorbedToken
		}
	}
	return out
}

// measureLayoutPair runs one construct's differential.
func measureLayoutPair(t *testing.T, lang treesitter.Language, probe layoutPair) layoutVerdict {
	t.Helper()
	v := layoutVerdict{lang: string(lang)}

	multi, multiSrc, ok := parseClean(t, lang, probe.multiLine)
	if !ok {
		v.skip = "the multi-line spelling does not parse cleanly under this grammar, so no differential can be taken"
		return v
	}
	defer multi.Close()
	single, singleSrc, ok := parseClean(t, lang, probe.singleLine)
	if !ok {
		v.skip = "the one-line spelling does not parse cleanly under this grammar, so no differential can be taken"
		return v
	}
	defer single.Close()

	// The SECOND differential, measured independently of the verdict below: a
	// token present in both spellings whose text differs only by surrounding
	// whitespace. The whitespace-only detector cannot observe this — "\n<" is
	// not whitespace-only — so the two never overlap.
	if absorbed := absorbedWhitespaceTokens(multi.RootNode(), multiSrc, single.RootNode(), singleSrc); len(absorbed) > 0 {
		v.absorbed = true
		v.absorbedToken = absorbed[0]
	}

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
