// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestQualifiedMemberBinding covers the two rungs that reach a declaration a
// bare top-level lookup cannot see: the PARENT-KEYED half of the qualified
// import rung, and the fully-qualified path rung.
//
// IT LIVES IN ITS OWN FILE because resolve_walk_test.go is close enough to the
// 500-line block that adding these rows inline would breach it — the same
// reason the per-language arm cases live beside it.
//
// NO PROBE ARM IS REGISTERED FOR THE ECMASCRIPT ROWS, and that is deliberate
// rather than a shortcut. jsmodule's init has already installed the real arm for
// the whole test binary, so a RegisterBindsResolver here would SHADOW the arm
// under test, and an UnregisterBindsResolver in a cleanup would delete the
// production registration for every test running after it. Driving the real arm
// is also strictly stronger: the renamed row below proves Bind.Name is what
// jsmodule actually records, not what a probe was told to record.
func TestQualifiedMemberBinding(t *testing.T) {
	// KNOWN-POSITIVE CONTROL FOR THE WHOLE TEST. Every row below asserts
	// something about an index and a bind; this row proves both are LIVE, so a
	// green run cannot mean the fixtures produced an empty index and the
	// assertions never had anything to disagree with.
	t.Run("control_bare_import_binds_top_level", func(t *testing.T) {
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "web/lib.ts", src: "export function helper(): number {\n  return 1;\n}\n"},
			{path: "web/main.ts", src: "" +
				"import {helper} from './lib';\n\n" +
				"export function run(): number {\n  return helper();\n}\n"},
		}, "web/main.ts", "helper")

		require.NotNil(t, e.Ref)
		require.Equal(t, "file:web/lib.ts", e.Ref.Binds["helper"].Scope,
			"the production ECMAScript arm must reach the reference site")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/lib.ts:helper", got.Candidates[0].NodeID)
	})

	t.Run("imported_class_member_binds", func(t *testing.T) {
		// THE HEADLINE ROW. `Foo.method()` where Foo is an imported class: the
		// member is indexed with Parent "Foo", so the landed top-level lookup
		// misses it and the reference used to fall through to the dynamic rung.
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "web/foo.ts", src: "" +
				"export class Foo {\n  static method(): number {\n    return 1;\n  }\n}\n"},
			{path: "web/main.ts", src: "" +
				"import {Foo} from './foo';\n\n" +
				"export function run(): number {\n  return Foo.method();\n}\n"},
		}, "web/main.ts", "Foo.method")

		require.NotNil(t, e.Ref)
		require.Equal(t, "file:web/foo.ts", e.Ref.Binds["Foo"].Scope)

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedMember, got.Rule)
		assert.Equal(t, "qualified-member", string(got.Rule),
			"the rule's recorded VALUE is what a resolution audit reads")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/foo.ts:Foo.method", got.Candidates[0].NodeID,
			"the member binds in the IMPORTED file, under its own container")
	})

	t.Run("renamed_import_member_binds", func(t *testing.T) {
		// THE ROW THAT PROVES THE PARENT KEY IS bind.Name AND NOT THE QUALIFIER.
		// The reference writes Bar; nothing anywhere is parented to a container
		// called Bar, so a parent key taken from the qualifier misses and this
		// row is the only thing that catches it.
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "web/foo.ts", src: "" +
				"export class Foo {\n  static method(): number {\n    return 1;\n  }\n}\n"},
			{path: "web/main.ts", src: "" +
				"import {Foo as Bar} from './foo';\n\n" +
				"export function run(): number {\n  return Bar.method();\n}\n"},
		}, "web/main.ts", "Bar.method")

		require.NotNil(t, e.Ref)
		require.Equal(t, "Foo", e.Ref.Binds["Bar"].Name,
			"the alias renames the reference, never the declaration")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedMember, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/foo.ts:Foo.method", got.Candidates[0].NodeID)
	})

	t.Run("namespace_member_still_top_level", func(t *testing.T) {
		// THE CHARACTERIZATION GUARD. `import * as ns` renames the MODULE and
		// not its members, so the bind's Name override must never reach the NAME
		// key. This row goes red the moment someone applies it there: the lookup
		// would ask for a declaration the module does not spell.
		//
		// It also pins the Parent-key fallback's harmless case: Name is empty for
		// a namespace import, so the parent key is "ns", nothing in the target is
		// parented to a container of that name, and the top-level lookup answers.
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "web/util.ts", src: "export function method(): number {\n  return 1;\n}\n"},
			{path: "web/main.ts", src: "" +
				"import * as ns from './util';\n\n" +
				"export function run(): number {\n  return ns.method();\n}\n"},
		}, "web/main.ts", "ns.method")

		require.NotNil(t, e.Ref)
		require.Empty(t, e.Ref.Binds["ns"].Name,
			"a namespace import carries no declared name — the fallback case")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedImport, got.Rule,
			"a namespace member is a TOP-LEVEL declaration and keeps the landed rule")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/util.ts:method", got.Candidates[0].NodeID)
	})

	t.Run("both_hit_is_a_closed_group", func(t *testing.T) {
		// THE HONESTY ROW, AND IT MUST NOT BE SATISFIABLE BY A PICKED WINNER.
		// The module exports BOTH a top-level `method` and a class Foo with a
		// static `method`, so `Foo.method()` genuinely reaches two candidates and
		// the ladder has no type information to choose between them. The truthful
		// output is a CLOSED group — exactly one of these — at Confidence 1/N.
		both := []fixtureFile{
			{path: "web/both.ts", src: "" +
				"export function method(): number {\n  return 1;\n}\n\n" +
				"export class Foo {\n  static method(): number {\n    return 2;\n  }\n}\n"},
			{path: "web/main.ts", src: "" +
				"import {Foo} from './both';\n\n" +
				"export function run(): number {\n  return Foo.method();\n}\n"},
		}

		ix, e := resolveRepoFixtureRef(t, both, "web/main.ts", "Foo.method")
		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefAmbiguous, got.Status)
		assert.Equal(t, RuleQualifiedMember, got.Rule,
			"the union is attributed to the member rung, which records that the "+
				"top-level rival could not be excluded")
		require.Len(t, got.Candidates, 2)
		assert.Equal(t, []string{"web/both.ts:method", "web/both.ts:Foo.method"},
			[]string{got.Candidates[0].NodeID, got.Candidates[1].NodeID},
			"top-level first, so a group's member edges are byte-identical across collects")

		// THE EMITTED GROUP, which is what a reader of the graph actually sees.
		res := populateRepoFixture(t, both, nil)
		group := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.Len(t, group, 2, "one edge per candidate, never a narrowed guess")
		for _, ge := range group {
			assert.Equal(t, "web/main.ts:run", ge.FromId)
			assert.InDelta(t, 0.5, ge.Confidence, 1e-9, "Confidence is 1/N")
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, ge.Method)
		}
		keys := evidenceKeys(group)
		require.Len(t, keys, 1, "both members of one group share one key")
		for k := range keys {
			assert.NotEmpty(t, k)
		}
	})

	t.Run("member_absent_falls_through", func(t *testing.T) {
		// THE ROW THAT FORBIDS A MANUFACTURED MEMBER EDGE. The bound scope holds
		// neither a top-level `method` nor a Foo.method, so the new lookup must
		// contribute NOTHING and the later rungs must decide — here the dynamic
		// rung, reaching the referencing file's own `method`.
		//
		// THE LOCAL DECLARATION IS THE POINT: without it the row would pass on an
		// empty tail of the ladder and prove nothing about which rung answered.
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "web/other.ts", src: "" +
				"export class Foo {\n  static other(): number {\n    return 1;\n  }\n}\n"},
			{path: "web/main.ts", src: "" +
				"import {Foo} from './other';\n\n" +
				"export function method(): number {\n  return 7;\n}\n\n" +
				"export function run(): number {\n  return Foo.method();\n}\n"},
		}, "web/main.ts", "Foo.method")

		require.NotNil(t, e.Ref)
		require.True(t, ix.hasScope(e.Ref.Binds["Foo"].Scope),
			"the bound scope IS indexed — this row is about a missing member, not a missing scope")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleQualifiedMember, got.Rule,
			"an absent member must not be manufactured")
		assert.Equal(t, RefDynamic, got.Status)
		assert.Equal(t, RuleDynamicScope, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/main.ts:method", got.Candidates[0].NodeID,
			"the later rungs decide, exactly as they did before the member lookup existed")
	})

	t.Run("static_member_import_binds", func(t *testing.T) {
		// THE LADDER HALF OF Bind.Container, and the reason it carries its own
		// gate: if the arm records the container but the UNQUALIFIED import rung
		// never reads it, the arm's own test stays green and only this row goes
		// red. The two exist to catch opposite halves of one change.
		//
		// The reference is a BARE `d()` — a static-member import binds a name
		// with no qualifier — so it reaches the unqualified import rung, not the
		// qualified one, and still resolves to a PARENTED declaration.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "a/b/C.java", src: "" +
				"package a.b;\n\nclass C {\n    static int d() { return 1; }\n}\n"},
			{path: "app/Main.java", src: "" +
				"package app;\n\nimport static a.b.C.d;\n\n" +
				"class Main {\n    int go() { return d(); }\n}\n"},
		}, "app/Main.java", treesitter.EdgeCalls, "d")

		require.NotNil(t, e.Ref)
		require.Equal(t, "ns:java:a_b", e.Ref.Binds["d"].Scope)
		require.Equal(t, "C", e.Ref.Binds["d"].Container,
			"the arm must have recorded the container for the rung to have anything to read")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedMember, got.Rule,
			"the rung differs but the fact recorded is the same one, so the constant is shared")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "a/b/C.java:C.d", got.Candidates[0].NodeID,
			"the bind keys d while the declaration satisfying it is parented to C")
	})

	qualifiedPathCases(t)
}
