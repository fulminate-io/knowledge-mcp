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
	// allowDelims skips ONLY the four delimiter legs of the blanket loop below
	// for this fixture. It is legitimate for a grammar whose callee is a shell
	// COMMAND WORD rather than a name, where `[` and `]` are ordinary characters
	// inside a quoted expansion — and for nothing else. The newline leg and the
	// surrounding-whitespace legs still run for every fixture without exception.
	allowDelims bool
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
// line is still what makes the no-delimiter assertion real: no unchained
// fixture can put a parenthesis into a composed span, so without it that
// assertion is vacuous. A CORRECT build now emits NOTHING for the chained tail
// rather than the bare trailing name, because the tail's receiver is the result
// of the inner call and a bare name binds by NAME into the caller's own scope.
// The assertion still catches a broken cut: a cut that fails to reduce emits
// `a.b(1).c`, which is NOT bare, is therefore NOT declined, and trips the
// no-delimiter legs. It is also lua's catcher — the pinned lua grammar nests a
// chained call's inner call as the outer call's first prefix child, so a query
// missing the nested arm loses the inner `a.b`.
//
// java, ruby and python additionally carry a SUBSCRIPTED receiver, which is the
// only shape that exercises the bracket half of the same assertion. Its
// trailing name is now stated NEGATIVELY in absent — it reaches the identical
// cut through the bracket half of one test and is declined for the identical
// reason — while the subscript's purpose here is unchanged.
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
				if !f.allowDelims {
					assert.NotContains(t, to, "(", "argument text leaked into a callee")
					assert.NotContains(t, to, ")", "argument text leaked into a callee")
					assert.NotContains(t, to, "[", "subscript text leaked into a callee")
					assert.NotContains(t, to, "]", "subscript text leaked into a callee")
				}
				assert.NotContains(t, to, "\n", "a callee must not span lines")
				assert.Equal(t, strings.TrimSpace(to), to,
					"a callee carries no surrounding whitespace")
			}
		})
	}
}

// calleeFixtures is the per-language table. The chained call contributes `a.b`,
// its inner call; its OUTER call is declined, because reducing it past the
// argument list leaves a bare name whose receiver the emission threw away.
// The ECMAScript constructor fixtures are appended next, and the receiver-shape
// table last; those two are the only groups that name their own subtests.
func calleeFixtures() []calleeFixture {
	all := append(wrapperNodeFixtures(), spanCompositionFixtures()...)
	all = append(all, ecmaConstructorFixtures()...)
	// The receiver-shape table lives in queries_capture_literal_test.go, which
	// keeps this file under the repository's per-file line block the same way
	// resolve_walk_test.go delegates its ladder arms to sibling files.
	return append(all, calleeLiteralFixtures()...)
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
//	new Gamma(3).run()   chained constructor call, asserted as an INVARIANT and
//	                     never as an exact set: BOTH `new Gamma(3).run` and the
//	                     bare `run` are absent. The span is still reduced past
//	                     the argument list, and what survives is a bare name on a
//	                     constructor receiver, which is declined rather than
//	                     emitted. The set still gains `Gamma`, which is the
//	                     constructor arm doing its job and is this line's real
//	                     subject.
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
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "sink", "Delta"},
			absent: []string{"new Gamma(3).run", "run"}},
		{lang: LangTSX, name: "tsx_constructor", path: "a/ctor.tsx",
			src:    ecmaConstructorSrc,
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "sink", "Delta"},
			absent: []string{"new Gamma(3).run", "run"}},
		{lang: LangJavaScript, name: "javascript_constructor", path: "a/ctor.js",
			src:    ecmaConstructorSrc,
			want:   []string{"Alpha", "nsAlias.Beta", "Gamma", "sink", "Delta"},
			absent: []string{"new Gamma(3).run", "run"}},
	}
}

// wrapperNodeFixtures are the languages whose grammar has a single node whose
// text IS the qualified callee.
func wrapperNodeFixtures() []calleeFixture {
	return []calleeFixture{
		{lang: LangCSharp, path: "a/R.cs",
			src:    "class Runner {\n    void Go() {\n        obj.DoThing(1);\n        plain(3);\n        a.b(1).c(2);\n    }\n}\n",
			want:   []string{"obj.DoThing", "plain", "a.b"},
			absent: []string{"c"}},
		{lang: LangPython, path: "a/r.py",
			src:    "def go():\n    obj.do_thing(1)\n    plain(3)\n    a.b(1).c(2)\n    d['k'].method()\n",
			want:   []string{"obj.do_thing", "plain", "a.b"},
			absent: []string{"c", "method"}},
		{lang: LangKotlin, path: "a/R.kt",
			src:    "fun go() {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n}\n",
			want:   []string{"obj.doThing", "plain", "a.b"},
			absent: []string{"c"}},
		{lang: LangSwift, path: "a/R.swift",
			src:    "func go() {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n}\n",
			want:   []string{"obj.doThing", "plain", "a.b"},
			absent: []string{"c"}},
		{lang: LangScala, path: "a/R.scala",
			src:    "object Runner {\n  def go(): Unit = {\n    obj.doThing(1)\n    plain(3)\n    a.b(1).c(2)\n  }\n}\n",
			want:   []string{"obj.doThing", "plain", "a.b"},
			absent: []string{"c"}},
		{lang: LangGroovy, path: "a/R.groovy",
			src:    "class Runner {\n    def go() {\n        obj.doThing(1)\n        plain(3)\n        a.b(1).c(2)\n    }\n}\n",
			want:   []string{"obj.doThing", "plain", "a.b"},
			absent: []string{"c"}},
		{lang: LangRust, path: "a/r.rs",
			src: "fn go() {\n    obj.do_thing(1);\n    plain(3);\n    foo::bar(2);\n    a.b(1).c(2);\n}\n",
			// foo::bar is the scope-operator arm's own catcher: that call
			// emitted nothing whatsoever before the arm existed.
			want:   []string{"obj.do_thing", "plain", "foo::bar", "a.b"},
			absent: []string{"c"}},
		{lang: LangCPP, path: "a/r.cpp",
			src:    "void go() {\n    obj.m(1);\n    plain(4);\n    ns::g(3);\n    a.b(1).c(2);\n}\n",
			want:   []string{"obj.m", "plain", "ns::g", "a.b"},
			absent: []string{"c"}},
		{lang: LangElixir, path: "a/r.ex",
			src: "defmodule Runner do\n  def go do\n    Enum.map([], fn x -> x end)\n    plain(3)\n  end\nend\n",
			// The macro keywords are the point: every Elixir module used to
			// emit CALLS edges to its own definition macros.
			want:   []string{"Enum.map", "plain"},
			absent: []string{"def", "defmodule"}},

		// THE TWO MULTI-LINE FIXTURES. They need no new assertion machinery:
		// TestQualifiedCalleeCapture already asserts every emitted callee spans
		// no line and carries no surrounding whitespace, and those assertions
		// passed only because every other fixture is single-line.
		//
		// The Go fixture covers two shapes: the chained tail whose line break
		// follows the dot (`b.Section(1).\n\t\tBuild(2)`), and the multi-line
		// qualified name with no intervening call (`recv.\n\t\tmethod(3)`),
		// where the span holds no parenthesis so the tail cut never fires at
		// all. `b.Section` is the characterization guard — green before and
		// after — proving the receiver half of the chain is not lost.
		//
		// Go cannot carry the third shape: automatic semicolon insertion
		// terminates a statement after `)` at end of line, so a leading-dot
		// continuation is not one chain at all. That shape lives in the
		// TypeScript fixture below.
		{lang: LangGo, name: "go_multiline_chain", path: "a/chain.go",
			src: "package chain\n\nfunc chainCaller() {\n\tb.Section(1).\n\t\tBuild(2)\n\trecv.\n\t\tmethod(3)\n}\n",
			// `Build` is a CHAINED TAIL whose receiver is the result of
			// `b.Section(1)` — a type this layer cannot know — so it is declined
			// rather than emitted as a bare name. The no-whitespace property
			// this fixture was landed to prove is UNCHANGED and is still proven
			// by `b.Section` and `recv.method`, each a multi-line qualified name
			// that would carry a line break if the strip regressed.
			want:   []string{"b.Section", "recv.method"},
			absent: []string{"Build"}},

		// THE THREE CALL LINES COVER THREE DIFFERENT THINGS, and only the third
		// pins the ordering inside the fix:
		//
		//	builder.section(1).\n    build(2)  shape 1, chained tail; the line
		//	                                   break FOLLOWS the dot.
		//	recv\n    .method(3)               shape 2, multi-line qualified
		//	                                   name; no intervening call, so the
		//	                                   tail cut never fires.
		//	page.locator('a')\n    .filter(4)  shape 1 again, but the line break
		//	                                   comes BEFORE the dot. THE ORDERING
		//	                                   DISCRIMINATOR.
		//
		// Only the third separates the two possible implementations. Its tail
		// after the last `)` is "\n    .filter", whose FIRST character is the
		// newline — so a separator TrimLeft running BEFORE the whitespace strip
		// stops immediately and leaves ".filter": whitespace-free, so the
		// standing census still reads zero, and unbindable, so the defect
		// survives the gate built to catch it.
		//
		// `build` and `filter` are CHAINED TAILS and are now declined rather
		// than emitted, so they are stated in absent. A reversed implementation
		// is still red: it leaves ".filter", which is NOT bare and so is never
		// declined, and the no-delimiter and no-whitespace legs of the blanket
		// loop plus the surviving qualified wants below catch it.
		//
		// `builder.section`, `recv.method` and `page.locator` are
		// characterization guards — emitted at baseline too, and present so the
		// fixture proves the receiver half of each chain survives.
		{lang: LangTypeScript, name: "typescript_multiline_chain", path: "a/chain.ts",
			src:    "function chainCaller() {\n  builder.section(1).\n    build(2);\n  recv\n    .method(3);\n  page.locator('a')\n    .filter(4);\n}\n",
			want:   []string{"builder.section", "recv.method", "page.locator"},
			absent: []string{"build", "filter"}},
	}
}

// spanCompositionFixtures are the languages whose grammar flattens the
// qualified callee into siblings, so the callee only exists as a composed span.
func spanCompositionFixtures() []calleeFixture {
	return []calleeFixture{
		{lang: LangJava, path: "a/R.java",
			src: "class Runner {\n    void go() {\n        obj.doThing(1);\n        plain(3);\n" +
				"        this.x.y.deep(4);\n        a.b(1).c(2);\n        arr[0].size();\n    }\n}\n",
			want:   []string{"obj.doThing", "plain", "this.x.y.deep", "a.b"},
			absent: []string{"c", "size"}},
		{lang: LangRuby, path: "a/r.rb",
			src:    "def go\n  obj.do_thing(1)\n  plain(3)\n  a.b(1).c(2)\n  h[:k].to_s\nend\n",
			want:   []string{"obj.do_thing", "plain", "a.b"},
			absent: []string{"c", "to_s"}},
		{lang: LangPHP, path: "a/r.php",
			src:    "<?php\nfunction go() {\n    $o->doThing(1);\n    plain(3);\n    Bar::stat(2);\n    $a->b(1)->c(2);\n}\n",
			want:   []string{"$o->doThing", "plain", "Bar::stat", "$a->b"},
			absent: []string{"c"}},

		// LUA'S TAIL IS NOT PRODUCED BY THE CUT and needs the other mechanism.
		// The pinned grammar nests the inner call as the outer call's first
		// child and the Calls query captures only the trailing identifier, so
		// the composed span is `c` alone and no parenthesis ever enters it — the
		// cut never fires, and only a structural read of the capture's ancestors
		// can tell that a receiver was elided.
		//
		// `outer(inner(1))` IS A CHARACTERIZATION GUARD, green before and after,
		// and its whole value is in the failure direction. Lua's receiver-wrapper
		// node kind is `function_call`, which is ALSO the kind that encloses an
		// argument-position call, so an ancestor walk that does not END at the
		// argument node climbs out of the argument list, finds the CALLER's call
		// starting earlier than the span, and declines `inner` — a legitimate
		// call, deleted, with nothing else in the repository to notice. Measured
		// on the real tree: the capture, its own call and the enclosing argument
		// node ALL start at the same byte, so the argument node cannot be
		// distinguished by offset — only by kind. `plain` in the existing
		// `a.b(1).c(plain(2))` shape is the same case; keeping every one of
		// these names in want makes either regression a red subtest.
		{lang: LangLua, path: "a/r.lua",
			src:    "function go()\n  obj.doThing(1)\n  plain(3)\n  a.b(1).c(2)\n  outer(inner(1))\nend\n",
			want:   []string{"obj.doThing", "plain", "a.b", "inner", "outer"},
			absent: []string{"c"}},
	}
}
