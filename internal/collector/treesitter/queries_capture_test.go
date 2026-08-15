// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sitter "github.com/smacker/go-tree-sitter"
)

// TestCalleeCaptureNameCensus RE-DERIVES the single-capture-name property from
// the query sources every time it runs, rather than trusting a list.
//
// It is what makes span composition's behavior-preservation claim a CHECKED
// fact: the composed callee is the span across the captures NAMED `callee`, so
// a Calls query that introduced a second capture name would have that capture
// silently dropped from the composition. Go, TypeScript, TSX and JavaScript are
// edited by sibling tickets, and this keeps checking them.
func TestCalleeCaptureNameCensus(t *testing.T) {
	checked := 0
	for lang, entry := range registry {
		qs := entry.Queries()
		if qs == nil || qs.Calls == "" {
			continue
		}
		q, err := sitter.NewQuery([]byte(qs.Calls), entry.lang)
		require.NoError(t, err, "%s Calls query must compile", lang)
		for i := range int(q.CaptureCount()) {
			assert.Equal(t, "callee", q.CaptureNameForId(uint32(i)),
				"%s binds a Calls capture that span composition would drop", lang)
		}
		q.Close()
		checked++
	}
	// THE FLOOR IS MEASURED, NOT GUESSED, and it is what stops this test
	// passing vacuously: a census that walked an empty registry, or one whose
	// Queries() came back nil, would assert nothing at all. Nineteen registered
	// languages carry a non-empty Calls query today — go, typescript, tsx,
	// javascript, python, java, rust, c, cpp, csharp, ruby, php, swift, kotlin,
	// scala, elixir, lua, bash and groovy — and the remaining thirteen are the
	// markup and config grammars that declare none. A floor rather than an
	// equality so adding a language cannot false-fail it.
	require.GreaterOrEqual(t, checked, 19,
		"the census must have walked the registry, not an empty set")
}

// calleeFixture is one language's capture case.
type calleeFixture struct {
	lang Language
	// name overrides the subtest name. Empty means the Language constant's
	// value, which is what every single-fixture-per-language case uses. It is
	// set only where one language carries MORE than one fixture, so each is
	// greppable on its own.
	name string
	path string
	src  string
	// want is every callee the fixture must produce, stated explicitly rather
	// than as a count so a fixture emitting two of the WRONG things cannot pass.
	want []string
	// absent are callees that must NOT appear — the definition macros elixir
	// used to emit as call targets.
	absent []string
}

// subtestName is the Language constant's value unless the fixture named itself.
func (f calleeFixture) subtestName() string {
	if f.name != "" {
		return f.name
	}
	return string(f.lang)
}

// calleeTargets returns the CALLS edge targets one fixture produced.
func calleeTargets(t *testing.T, f calleeFixture) []string {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()

	res, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
	require.NoError(t, err)

	var out []string
	for _, e := range res.Edges {
		if e.Type == EdgeCalls {
			out = append(out, e.ToID)
		}
	}
	return out
}

// TestQualifiedCalleeCapture asserts that a qualified call's FULL text survives
// into the emitted callee, for every language this ticket touches.
//
// EVERY FIXTURE CARRIES A CHAINED CALL — `a.b(1).c(2)` in the per-language
// fixtures, `new Gamma(3).run()` in the ECMAScript constructor ones — and that
// line is what makes the no-delimiter assertion real: no unchained fixture can
// produce a parenthesis, so without it the assertion is vacuous. It is also
// lua's catcher — the pinned lua grammar nests a chained call's inner call as
// the outer call's first prefix child, so a query missing the nested arm emits
// ONE callee where the fixture expects two.
//
// java, ruby and python additionally carry a SUBSCRIPTED receiver, which is the
// only shape that exercises the bracket half of the same assertion, and whose
// expected trailing name is stated positively in want.
//
// THE QUALIFIER AND THE BARE NAME ARE ALWAYS DIFFERENT IDENTIFIERS, so an
// assertion on the qualified text cannot be satisfied by a capture that only
// ever saw the bare one.
func TestQualifiedCalleeCapture(t *testing.T) {
	for _, f := range calleeFixtures() {
		t.Run(f.subtestName(), func(t *testing.T) {
			got := calleeTargets(t, f)
			require.NotEmpty(t, got, "fixture produced no callees at all")

			for _, want := range f.want {
				assert.Contains(t, got, want)
			}
			for _, absent := range f.absent {
				assert.NotContains(t, got, absent)
			}
			for _, to := range got {
				assert.NotContains(t, to, "(", "argument text leaked into a callee")
				assert.NotContains(t, to, ")", "argument text leaked into a callee")
				assert.NotContains(t, to, "[", "subscript text leaked into a callee")
				assert.NotContains(t, to, "]", "subscript text leaked into a callee")
				assert.NotContains(t, to, "\n", "a callee must not span lines")
				assert.Equal(t, strings.TrimSpace(to), to,
					"a callee carries no surrounding whitespace")
			}
		})
	}
}

// calleeFixtures is the per-language table. The chained call contributes BOTH
// `a.b` (its inner call) and `c` (its outer, reduced past the argument list).
// The ECMAScript constructor fixtures are appended last and are the only
// entries that name their own subtest.
func calleeFixtures() []calleeFixture {
	all := append(wrapperNodeFixtures(), spanCompositionFixtures()...)
	return append(all, ecmaConstructorFixtures()...)
}

// ecmaConstructorSrc is the constructor fixture shared by typescript, tsx and
// javascript. One source serves all three because the three registrations must
// behave identically: typescript and tsx run the SAME query set against two
// grammars, and javascript runs a byte-identical copy against a third.
//
// The four call lines each catch something different:
//
//	new Alpha(1)         bare constructor — an imported class by bare name.
//	new nsAlias.Beta(2)  qualified constructor — the whole `nsAlias.Beta` must
//	                     survive to the callee, since stripping it to `Beta`
//	                     is what kept qualified constructors off the ladder.
//	new Gamma(3).run()   chained constructor call — a CHARACTERIZATION GUARD on
//	                     landed behavior, asserted as an INVARIANT and never as
//	                     an exact set: `run` is present and `new Gamma(3).run`
//	                     is not, because the fallback in extractCallEdges still
//	                     reduces the chained span. The set also gains `Gamma`,
//	                     which is the constructor arm doing its job.
//	sink(new Delta(4))   constructor in argument position — both `sink` and
//	                     `Delta` are captured and no argument text leaks into
//	                     any callee.
//
// EVERY IDENTIFIER IS DISTINCT and every one is declared or imported here, so
// no assertion can be satisfied by a capture that only ever saw another name.
const ecmaConstructorSrc = `import { Alpha } from './alpha';
import * as nsAlias from './ns';
import { Gamma } from './gamma';
import { Delta } from './delta';

function sink(value) {
  return value;
}

function go() {
  new Alpha(1);
  new nsAlias.Beta(2);
  new Gamma(3).run();
  sink(new Delta(4));
}
`

// ecmaConstructorFixtures covers the three languages whose Calls query gained
// the new_expression arm. Constructor references emitted no edge of ANY kind
// before it, so every want here is a callee the graph could not previously see.
func ecmaConstructorFixtures() []calleeFixture {
	return []calleeFixture{
		{lang: LangTypeScript, name: "typescript_constructor", path: "a/ctor.ts",
			src:    ecmaConstructorSrc,
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "run", "sink", "Delta"},
			absent: []string{"new Gamma(3).run"}},
		{lang: LangTSX, name: "tsx_constructor", path: "a/ctor.tsx",
			src:    ecmaConstructorSrc,
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "run", "sink", "Delta"},
			absent: []string{"new Gamma(3).run"}},
		{lang: LangJavaScript, name: "javascript_constructor", path: "a/ctor.js",
			src:    ecmaConstructorSrc,
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "run", "sink", "Delta"},
			absent: []string{"new Gamma(3).run"}},
	}
}

// wrapperNodeFixtures are the languages whose grammar has a single node whose
// text IS the qualified callee.
func wrapperNodeFixtures() []calleeFixture {
	return []calleeFixture{
		{lang: LangCSharp, path: "a/R.cs",
			src:  "class Runner {\n    void Go() {\n        obj.DoThing(1);\n        plain(3);\n        a.b(1).c(2);\n    }\n}\n",
			want: []string{"obj.DoThing", "plain", "a.b", "c"}},
		{lang: LangPython, path: "a/r.py",
			src:  "def go():\n    obj.do_thing(1)\n    plain(3)\n    a.b(1).c(2)\n    d['k'].method()\n",
			want: []string{"obj.do_thing", "plain", "a.b", "c", "method"}},
		{lang: LangKotlin, path: "a/R.kt",
			src:  "fun go() {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n}\n",
			want: []string{"obj.doThing", "plain", "a.b", "c"}},
		{lang: LangSwift, path: "a/R.swift",
			src:  "func go() {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n}\n",
			want: []string{"obj.doThing", "plain", "a.b", "c"}},
		{lang: LangScala, path: "a/R.scala",
			src:  "object Runner {\n  def go(): Unit = {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n  }\n}\n",
			want: []string{"obj.doThing", "plain", "a.b", "c"}},
		{lang: LangGroovy, path: "a/R.groovy",
			src:  "class Runner {\n    def go() {\n        obj.doThing(1)\n        plain(3)\n        a.b(1).c(2)\n    }\n}\n",
			want: []string{"obj.doThing", "plain", "a.b", "c"}},
		{lang: LangRust, path: "a/r.rs",
			src: "fn go() {\n    obj.do_thing(1);\n    plain(3);\n    foo::bar(2);\n    a.b(1).c(2);\n}\n",
			// foo::bar is the scope-operator arm's own catcher: that call
			// emitted nothing whatsoever before the arm existed.
			want: []string{"obj.do_thing", "plain", "foo::bar", "a.b", "c"}},
		{lang: LangCPP, path: "a/r.cpp",
			src:  "void go() {\n    obj.m(1);\n    plain(4);\n    ns::g(3);\n    a.b(1).c(2);\n}\n",
			want: []string{"obj.m", "plain", "ns::g", "a.b", "c"}},
		{lang: LangElixir, path: "a/r.ex",
			src: "defmodule Runner do\n  def go do\n    Enum.map([], fn x -> x end)\n    plain(3)\n  end\nend\n",
			// The macro keywords are the point: every Elixir module used to
			// emit CALLS edges to its own definition macros.
			want:   []string{"Enum.map", "plain"},
			absent: []string{"def", "defmodule"}},
	}
}

// spanCompositionFixtures are the languages whose grammar flattens the
// qualified callee into siblings, so the callee only exists as a composed span.
func spanCompositionFixtures() []calleeFixture {
	return []calleeFixture{
		{lang: LangJava, path: "a/R.java",
			src: "class Runner {\n    void go() {\n        obj.doThing(1);\n        plain(3);\n" +
				"        this.x.y.deep(4);\n        a.b(1).c(2);\n        arr[0].size();\n    }\n}\n",
			want: []string{"obj.doThing", "plain", "this.x.y.deep", "a.b", "c", "size"}},
		{lang: LangRuby, path: "a/r.rb",
			src:  "def go\n  obj.do_thing(1)\n  plain(3)\n  a.b(1).c(2)\n  h[:k].to_s\nend\n",
			want: []string{"obj.do_thing", "plain", "a.b", "c", "to_s"}},
		{lang: LangPHP, path: "a/r.php",
			src:  "<?php\nfunction go() {\n    $o->doThing(1);\n    plain(3);\n    Bar::stat(2);\n    $a->b(1)->c(2);\n}\n",
			want: []string{"$o->doThing", "plain", "Bar::stat", "$a->b", "c"}},
		{lang: LangLua, path: "a/r.lua",
			src:  "function go()\n  obj.doThing(1)\n  plain(3)\n  a.b(1).c(2)\nend\n",
			want: []string{"obj.doThing", "plain", "a.b", "c"}},
	}
}
