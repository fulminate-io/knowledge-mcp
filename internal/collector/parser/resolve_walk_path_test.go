// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// qualifiedPathCases carries the four rows that prove the fully-qualified rung.
// They are invoked from TestQualifiedMemberBinding so their subtest names read
// as that test's — the criteria grep them by that path — while their bodies
// live here, because resolve_walk_member_test.go is close enough to the
// 500-line block that adding them inline would breach it.
//
// A FULLY-QUALIFIED REFERENCE CARRIES NO IMPORT STATEMENT, so none of these rows
// registers or needs an arm: there is nothing for one to bind. That is also why
// the third row exists — it is the only one where a bind is present at all.
func qualifiedPathCases(t *testing.T) {
	t.Helper()

	t.Run("jvm_import_binds_through_the_declared_package", func(t *testing.T) {
		// THE CATCHER FOR THE OTHER HALF OF THE SCOPE-MODEL CHANGE, and it is
		// here because nothing else in the suite would have caught it. Giving
		// java, kotlin and scala ScopeDeclaredNamespace moves every declaration
		// in a PACKAGE-DECLARING file from a file scope to a namespace one — and
		// the JVM import arm derived its bind's scope from a FILE PATH, so the
		// two stopped naming the same string and the import rungs went silently
		// inert for essentially all real java. Measured both directions before
		// the arm was changed:
		//   arm deriving a path  -> bind "dir:a/b", index holds "ns:java:a_b",
		//                           resolution external/not-declared
		//   arm deriving the pkg -> bind "ns:java:a_b", resolution bound
		//
		// EVERY LANDED ARM PROOF USED A FIXTURE WITH NO PACKAGE DECLARATION,
		// which is not legal java to import from — you cannot import out of the
		// default package — so the whole suite agreed on a case real source never
		// takes. That is why this row declares one.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "a/b/C.java", src: "package a.b;\n\nclass C {\n    void go() {}\n}\n"},
			{path: "app/Main.java", src: "" +
				"package app;\n\nimport a.b.C;\n\nclass Main {\n    C field;\n}\n"},
		}, "app/Main.java", treesitter.EdgeUsesType, "C")

		require.NotNil(t, e.Ref)
		require.Equal(t, "ns:java:a_b", e.Ref.Binds["C"].Scope,
			"the arm binds into the PACKAGE, which is what the declaration is indexed under")
		require.True(t, ix.hasScope("ns:java:a_b"),
			"and the index genuinely holds that scope — the two must name one string")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "a/b/C.java:C", got.Candidates[0].NodeID)
	})

	t.Run("fq_in_repo_binds", func(t *testing.T) {
		// A declaration reached by its FULLY-QUALIFIED name. The qualifier is
		// the PACKAGE, which maps straight onto the declaring file's scope
		// because a package IS a namespace — no path is derived anywhere.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "com/acme/foo/Bar.java", src: "" +
				"package com.acme.foo;\n\nclass Bar {\n    void go() {}\n}\n"},
			{path: "app/Main.java", src: "" +
				"class Main {\n    com.acme.foo.Bar field;\n}\n"},
		}, "app/Main.java", treesitter.EdgeUsesType, "com.acme.foo.Bar")

		require.NotNil(t, e.Ref)
		require.Empty(t, e.Ref.Binds["com.acme.foo"].Scope,
			"nothing binds a package path — that is the whole reason this rung exists")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedPath, got.Rule)
		assert.Equal(t, "qualified-path", string(got.Rule),
			"the rule's recorded VALUE is what a resolution audit reads")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "com/acme/foo/Bar.java:Bar", got.Candidates[0].NodeID)
	})

	t.Run("fq_out_of_repo_terminates", func(t *testing.T) {
		// THE LOCAL DECLARATION IS THE POINT. `java.util.List` names a package
		// the index has never heard of, so the reference must TERMINATE — and
		// without a same-named local in the reference's own scope the row would
		// pass on an empty tail of the ladder and prove nothing, which is the
		// vacuity the whole defect was hiding behind.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "app/List.java", src: "class List {\n    void of() {}\n}\n"},
			{path: "app/Main.java", src: "" +
				"class Main {\n    java.util.List field;\n}\n"},
		}, "app/Main.java", treesitter.EdgeUsesType, "java.util.List")

		require.NotNil(t, e.Ref)
		require.False(t, ix.hasScope("ns:java:java_util"),
			"the derived scope must be one the index genuinely does not hold")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefExternal, got.Status)
		assert.Equal(t, RuleExternalQualifier, got.Rule,
			"the termination reuses the landed constant — the fact recorded is identical")
		assert.Empty(t, got.Candidates, "termination emits no edge to the local List at all")

		// KNOWN-POSITIVE CONTROL: the rival really is in the index, under the
		// reference's OWN scope, so the absence above is a resolution decision
		// rather than a declaration the fixture never produced.
		rival := ix.lookupScopeName(scopeNameKey{Scope: e.Ref.Scope, Name: "List"})
		require.Len(t, rival, 1, "the fixture must actually declare a competing local List")
		assert.Equal(t, "app/List.java:List", rival[0].NodeID)
	})

	t.Run("fq_bound_qualifier_unaffected", func(t *testing.T) {
		// THE CHARACTERIZATION GUARD FOR THE `!bound` CONDITION. A qualifier an
		// import DID bind is already answered above, and this row goes red if
		// the new rung is allowed to re-derive a scope for it.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "a/b/C.java", src: "" +
				"package a.b;\n\nclass C {\n    static void go() {}\n}\n"},
			{path: "app/Main.java", src: "" +
				"package app;\n\nimport a.b.C;\n\n" +
				"class Main {\n    void run() { C.go(); }\n}\n"},
		}, "app/Main.java", treesitter.EdgeCalls, "C.go")

		require.NotNil(t, e.Ref)
		require.NotEmpty(t, e.Ref.Binds["C"].Scope,
			"this is the one row whose qualifier IS bound")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleQualifiedPath, got.Rule,
			"a bound qualifier is answered by the import rung and never re-derived")
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedMember, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "a/b/C.java:C.go", got.Candidates[0].NodeID)
	})

	t.Run("package_not_directory", func(t *testing.T) {
		// THE ROW ONLY THE DECLARED-NAMESPACE MECHANISM CAN PASS, and therefore
		// the row that makes the ruling's REASON testable rather than merely
		// stated. Scala legally permits a package that does not match its
		// directory, and this fixture uses one: the declaration lives at
		// src/thing.scala while declaring `package com.acme.deep`. A
		// path-derivation oracle would look under com/acme/deep, find nothing,
		// and miss — which is precisely why that direction was rejected.
		//
		// IT IS A CALL AND NOT A TYPE REFERENCE, and that is forced rather than
		// chosen: scala's TypeRefs query is `(type_identifier) @typeref`, which
		// captures only the FINAL segment of `com.acme.deep.Thing`, so a scala
		// type reference never carries a qualifier at all. Its Calls query
		// captures a field_expression WHOLE, so a call is the one scala shape
		// that reaches this rung. Kotlin is the same story and worse — its
		// user_type holds one type_identifier PER SEGMENT — which is why this
		// row is scala.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "src/thing.scala", src: "" +
				"package com.acme.deep\n\ndef go(): Int = 1\n"},
			{path: "app/main.scala", src: "" +
				"class Main {\n  def run(): Int = com.acme.deep.go()\n}\n"},
		}, "app/main.scala", treesitter.EdgeCalls, "com.acme.deep.go")

		require.NotNil(t, e.Ref)
		require.Empty(t, e.Ref.Binds["com.acme.deep"].Scope,
			"nothing binds a package path")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedPath, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "src/thing.scala:go", got.Candidates[0].NodeID,
			"the declared package is what located it, not the directory it sits in")
	})
}
