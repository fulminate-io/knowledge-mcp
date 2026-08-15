// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// IT LIVES IN ITS OWN FILE rather than beside the ladder it extends:
// resolve_walk_test.go is already at 410 lines against the repository's hard
// 500-line block, and nine fixtures do not fit under it.

// siblingCase is one language's answer to the single question this table asks:
// DOES A BARE, RECEIVERLESS CALL INSIDE A MEMBER RESOLVE TO A SIBLING MEMBER OF
// THE SAME CONTAINER?
type siblingCase struct {
	name  string
	files []fixtureFile

	// from is the referencing declaration's node ID — the member whose body
	// writes the bare call.
	from string
	// target is the node ID the bare call would reach if the sibling rung
	// fired, except in the shadowing row where it is the top-level declaration
	// the reference must reach INSTEAD.
	target string
	// wantTarget is the language's own answer, derived by execution.
	wantTarget bool

	// control is a node ID the SAME fixture must also reach from the same
	// referencing declaration. An absence row is worthless without it: a
	// fixture that produced no edges at all would satisfy every "must not
	// bind" assertion in this table. Presence rows may leave it empty — their
	// own assertion is the positive.
	control string
}

// TestResolveRef_SiblingRungByLanguage drives the REAL chunker through
// populateFixture, one fixture per language, and pins both directions of the
// per-language sibling rung.
//
// EVERY ROW'S PROVENANCE IS MARKED EXECUTED OR CITED beside its fixture. go,
// python, javascript, ruby and java were RUN at the toolchains on this machine;
// typescript and tsx are CITED, inheriting the javascript execution by language
// identity. A reader must be able to tell which rows were run from which were
// reasoned, and the distinction is not recoverable from the code.
//
// THE THREE ECMASCRIPT ROWS ARE NOT REDUNDANT WITH EACH OTHER. The table
// criterion counts five SkipSiblingRung rows and cannot see a wrong constant —
// LangJavaScript written twice with LangTSX never written satisfies a count of
// five exactly. Only a fixture per language proves the row reached the language
// it names.
func TestResolveRef_SiblingRungByLanguage(t *testing.T) {
	cases := append(goSiblingCases(), ecmaSiblingCases()...)
	cases = append(cases, keepingSiblingCases()...)
	require.Len(t, cases, 9, "the table's own size is pinned: nine languages' answers")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := populateFixture(t, tc.files)

			// CONTROL, LEG ONE: the declaration the row talks about must exist
			// as a node. Without it an absence row could pass because the
			// fixture never declared the target at all.
			require.True(t, nodeIDSet(res)[tc.target],
				"control: the fixture must declare %s", tc.target)
			require.True(t, nodeIDSet(res)[tc.from],
				"control: the fixture must declare the referencing member %s", tc.from)

			// CONTROL, LEG TWO: the same referencing declaration reaches
			// something, so a fixture that resolved nothing cannot pass.
			if tc.control != "" {
				require.True(t, hasEdge(res, kgtypes.EdgeCalls, tc.from, tc.control),
					"control: %s must still bind %s — a fixture that bound nothing "+
						"would satisfy the absence assertion below", tc.from, tc.control)
			}

			got := hasEdge(res, kgtypes.EdgeCalls, tc.from, tc.target)
			if tc.wantTarget {
				assert.True(t, got, "%s must bind %s", tc.from, tc.target)
				return
			}
			assert.False(t, got,
				"%s must NOT bind %s: a bare call carries no implicit receiver in "+
					"this language, so the edge would state a call the language "+
					"itself rejects", tc.from, tc.target)
		})
	}
}

// goSiblingCases carries Go's three rows: the plain sibling shape, the builtin
// shape the resolution trace found, and the shadowing twin that keeps the fix
// from becoming a new wrong answer.
func goSiblingCases() []siblingCase {
	return []siblingCase{
		{
			// EXECUTED — go build on a struct with a method `a` and a method
			// whose body calls a bare `a()`:
			//   ./x.go:7:30: undefined: a
			// The call is not merely unconventional, it does not compile; the
			// collector reaches the shape anyway because it parses rather than
			// type-checks, which is exactly why the rung had to be gated
			// instead of left to the language.
			name: "go_bare_call_does_not_reach_sibling",
			files: []fixtureFile{{path: "app/main.go", src: "" +
				"package app\n\n" +
				"func topLevel() int { return 1 }\n\n" +
				"type Runner struct{}\n\n" +
				"func (r Runner) helper() int { return 2 }\n\n" +
				"func (r Runner) Walk() int { return helper() + topLevel() }\n"}},
			from:    "app/main.go:Runner.Walk",
			target:  "app/main.go:Runner.helper",
			control: "app/main.go:topLevel",
		},
		{
			// THE RESCOPED SECOND DEFECT, verbatim from the resolution trace: a
			// bare `append(...)` inside a method of a type that also declares an
			// `append` method bound to the sibling at full confidence, silently.
			// With the rung gated it falls to the own-scope rung, finds no
			// top-level append in the package, and lands external — which is the
			// honest answer for a call to a language builtin.
			//
			// NO BUILTIN SET IS ADDED ANYWHERE. The mis-bind is subsumed by the
			// gate rather than separately mechanized: nothing here knows the name
			// `append` is special, and that is the point.
			name: "go_builtin_append_falls_external",
			files: []fixtureFile{{path: "app/main.go", src: "" +
				"package app\n\n" +
				"func topLevel() int { return 1 }\n\n" +
				"type Buf struct{}\n\n" +
				"func (b Buf) append(x int) int { return x }\n\n" +
				"func (b Buf) Add(x int) int { return append(x) + topLevel() }\n"}},
			from:    "app/main.go:Buf.Add",
			target:  "app/main.go:Buf.append",
			control: "app/main.go:topLevel",
		},
		{
			// THE OTHER DIRECTION, AND A CHARACTERIZATION GUARD RATHER THAN A
			// RED-FIRST ROW: it is green before the gate and green after. Go
			// permits shadowing a builtin at package level, so a package that
			// declares its own top-level `append` genuinely has that declaration
			// in scope and the bare reference must still bind to it. Without this
			// row the fix could be "correct" by refusing to bind anything named
			// append, which is a new wrong answer rather than the removal of an
			// old one.
			//
			// BOTH FILES SIT IN ONE DIRECTORY DELIBERATELY. Go's scope unit is
			// dir:, so a declaration one directory over is in a different scope
			// and this row would pass for the wrong reason — the reference would
			// miss the declaration rather than reach it.
			//
			// AND THE FIXTURE DECLARES NO SIBLING `append` METHOD, which is what
			// keeps it green in both states: with a sibling present the rung
			// would have won before the gate and the top-level declaration after,
			// making it a red-first row and not the guard it is labeled.
			name: "go_shadowed_builtin_still_binds",
			files: []fixtureFile{
				{path: "app/decl.go", src: "" +
					"package app\n\n" +
					"func append(xs []int, x int) []int { return xs }\n\n" +
					"func topLevel() int { return 1 }\n"},
				{path: "app/main.go", src: "" +
					"package app\n\n" +
					"type Runner struct{}\n\n" +
					"func (r Runner) Add(xs []int) []int { _ = topLevel(); return append(xs, 1) }\n"},
			},
			from:       "app/main.go:Runner.Add",
			target:     "app/decl.go:append",
			wantTarget: true,
			control:    "app/decl.go:topLevel",
		},
	}
}

// ecmaSiblingCases carries python plus the three ECMAScript languages — the
// four remaining skip rows.
func ecmaSiblingCases() []siblingCase {
	return []siblingCase{
		{
			// EXECUTED — python3 on a class with a method `a` and a method whose
			// body calls a bare `a()`:
			//   NameError: name 'a' is not defined. Did you mean: 'self.a'?
			// The interpreter names the correction it would have needed, which is
			// as direct a statement as a language makes that the bare form does
			// not reach the sibling.
			name: "python_bare_call_does_not_reach_sibling",
			files: []fixtureFile{{path: "app/main.py", src: "" +
				"def top_level():\n" +
				"    return 1\n\n\n" +
				"class Runner:\n" +
				"    def helper(self):\n" +
				"        return 2\n\n" +
				"    def walk(self):\n" +
				"        return helper() + top_level()\n"}},
			from:    "app/main.py:Runner.walk",
			target:  "app/main.py:Runner.helper",
			control: "app/main.py:top_level",
		},
		{
			// EXECUTED — node on a class with a method `a` and a method whose
			// body calls a bare `a()`:
			//   ReferenceError: a is not defined
			name: "javascript_bare_call_does_not_reach_sibling",
			files: []fixtureFile{{path: "app/main.js", src: "" +
				"export function topLevel() { return 1; }\n\n" +
				"export class Runner {\n" +
				"  helper() { return 2; }\n" +
				"  walk() { return helper() + topLevel(); }\n" +
				"}\n"}},
			from:    "app/main.js:Runner.walk",
			target:  "app/main.js:Runner.helper",
			control: "app/main.js:topLevel",
		},
		{
			// CITED, NOT EXECUTED — inherits the javascript row by language
			// identity: TypeScript's class semantics are JavaScript's and a bare
			// call in a method has no implicit receiver in either.
			name: "typescript_bare_call_does_not_reach_sibling",
			files: []fixtureFile{{path: "app/main.ts", src: "" +
				"export function topLevel(): number { return 1; }\n\n" +
				"export class Runner {\n" +
				"  helper(): number { return 2; }\n" +
				"  walk(): number { return helper() + topLevel(); }\n" +
				"}\n"}},
			from:    "app/main.ts:Runner.walk",
			target:  "app/main.ts:Runner.helper",
			control: "app/main.ts:topLevel",
		},
		{
			// CITED, NOT EXECUTED — the collector's tsx files are TypeScript, so
			// this row inherits the same execution through the same identity.
			name: "tsx_bare_call_does_not_reach_sibling",
			files: []fixtureFile{{path: "app/main.tsx", src: "" +
				"export function topLevel(): number { return 1; }\n\n" +
				"export class Runner {\n" +
				"  helper(): number { return 2; }\n" +
				"  walk(): number { return helper() + topLevel(); }\n" +
				"}\n"}},
			from:    "app/main.tsx:Runner.walk",
			target:  "app/main.tsx:Runner.helper",
			control: "app/main.tsx:topLevel",
		},
	}
}

// keepingSiblingCases carries the two languages that KEEP the rung.
//
// THIS IS THE DIRECTION THAT CATCHES AN OVER-BROAD FIX. A change that skipped
// the sibling rung for every language passes all seven absence rows above and
// fails only here, which is the only reason those seven mean anything.
func keepingSiblingCases() []siblingCase {
	return []siblingCase{
		{
			// EXECUTED — ruby on a class with a method `a` and a method whose
			// body calls a bare `a()`: the call RUNS and prints, because the
			// receiverless form dispatches on the implicit self.
			name: "ruby_bare_call_reaches_sibling",
			files: []fixtureFile{{path: "app/main.rb", src: "" +
				"def top_level\n" +
				"  1\n" +
				"end\n\n" +
				"class Runner\n" +
				"  def helper\n" +
				"    2\n" +
				"  end\n\n" +
				"  def walk\n" +
				"    helper() + top_level()\n" +
				"  end\n" +
				"end\n"}},
			from:       "app/main.rb:Runner.walk",
			target:     "app/main.rb:Runner.helper",
			wantTarget: true,
			control:    "app/main.rb:top_level",
		},
		{
			// EXECUTED — javac then java on a class with a method `a` and a
			// method whose body calls a bare `a()`: it COMPILES and runs, because
			// the receiverless form dispatches on the implicit this.
			//
			// NO CONTROL EDGE IS SET, and the omission is deliberate rather than
			// an oversight: Java has no top-level functions, so there is no
			// second bare call in one file to bind. This row asserts a PRESENCE,
			// which is its own known-positive — an empty fixture fails it.
			name: "java_bare_call_reaches_sibling",
			files: []fixtureFile{{path: "app/Main.java", src: "" +
				"class Runner {\n" +
				"  int helper() { return 2; }\n" +
				"  int walk() { return helper(); }\n" +
				"}\n"}},
			from:       "app/Main.java:Runner.walk",
			target:     "app/Main.java:Runner.helper",
			wantTarget: true,
		},
	}
}
