// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// These tests drive the real chunker through chunkResultsToPopulate — the same
// harness populate_resolve_test.go uses — so they measure what survives
// resolveEdges rather than what the chunker emitted. Every expectation is
// derived from the node set the run produced; no path hash is written down,
// because a hash literal would pin the fixture's byte layout instead of the
// rule under test.

// containsParents returns every CONTAINS edge source pointing at to except the
// file itself, i.e. the parent-to-member edges. Asserting this set by EQUALITY
// is what distinguishes the right pairing from any pairing: with two same-named
// containers in one file, an assertion that merely finds some parent-to-member
// edge is satisfied by the wrong container.
func containsParents(res PopulateResult, to, filePath string) []string {
	var out []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeContains && e.ToId == to && e.FromId != filePath {
			out = append(out, e.FromId)
		}
	}
	return out
}

// enclosingContainer returns the ID of the one container-kinded node whose line
// range encloses the member's — the container that lexically encloses it, and
// so the declaration its parent-to-member edge must name. Every fixture below
// puts its two same-named containers on different lines precisely so this
// resolves to exactly one, which the require.Len enforces rather than assumes.
func enclosingContainer(t *testing.T, res PopulateResult, memberID string, kinds ...string) string {
	t.Helper()
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	member := nodeByID(res.Nodes, memberID)
	require.NotNil(t, member, "member %q is in the populate result", memberID)

	var found []string
	for _, n := range res.Nodes {
		if n.Id == memberID || n.FilePath != member.FilePath || !want[n.Type] {
			continue
		}
		if n.StartLine <= member.StartLine && n.EndLine >= member.EndLine {
			found = append(found, n.Id)
		}
	}
	require.Len(t, found, 1, "exactly one %v encloses %q", kinds, memberID)
	return found[0]
}

// assertMemberAttaches asserts the member's only parent-to-member CONTAINS edge
// names the container that encloses it, and returns that container's ID so a
// caller can check two members took DIFFERENT containers.
func assertMemberAttaches(t *testing.T, res PopulateResult, path, memberID string, kinds ...string) string {
	t.Helper()
	parent := enclosingContainer(t, res, memberID, kinds...)
	assert.Equal(t, []string{parent}, containsParents(res, memberID, path),
		"%q attaches to the container enclosing it and to nothing else", memberID)
	return parent
}

// TestCollidingContainerEdgeResolves proves the parent-to-member CONTAINS edge
// survives resolveEdges when two containers in one file share a name. The four
// collision subtests are the fix; the two controls are the known positives that
// keep a passing collision case from meaning "this edge resolves for everyone"
// — they carry no collision at all and must resolve either way.
func TestCollidingContainerEdgeResolves(t *testing.T) {
	t.Run("rust_struct_and_impl", func(t *testing.T) {
		// A struct beside its impl is the ordinary shape of every Rust type.
		// The impl is the enclosing block, so the method attaches THERE and not
		// to the struct chunk that shares its name.
		const path = "pkg/si.rs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "pub struct Thing;\nimpl Thing { pub fn method(&self) {} }\n",
		}})
		parent := assertMemberAttaches(t, res, path, "pkg/si.rs:Thing.method", "struct_item", "impl_item")
		assert.Equal(t, "impl_item", nodeByID(res.Nodes, parent).Type, "the impl won, not the struct")
	})

	t.Run("rust_impl_only_control", func(t *testing.T) {
		// NO COLLISION: one impl, no struct sharing its name.
		const path = "pkg/b.rs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "impl Widget { pub fn method2(&self) {} }\n",
		}})
		assertMemberAttaches(t, res, path, "pkg/b.rs:Widget.method2", "impl_item")
	})

	t.Run("cpp_reopened_namespace", func(t *testing.T) {
		// Reopening a namespace in one file is routine C++. Each block keeps
		// its own member.
		const path = "pkg/re.cpp"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "namespace app { void a() {} }\nnamespace app { void b() {} }\n",
		}})
		first := assertMemberAttaches(t, res, path, "pkg/re.cpp:app.a", "namespace_definition")
		second := assertMemberAttaches(t, res, path, "pkg/re.cpp:app.b", "namespace_definition")
		assert.NotEqual(t, first, second, "the two blocks are distinct containers")
	})

	t.Run("cpp_single_namespace_control", func(t *testing.T) {
		// NO COLLISION: one namespace holding both members.
		const path = "pkg/one.cpp"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "namespace app { void a() {} void b() {} }\n",
		}})
		first := assertMemberAttaches(t, res, path, "pkg/one.cpp:app.a", "namespace_definition")
		second := assertMemberAttaches(t, res, path, "pkg/one.cpp:app.b", "namespace_definition")
		assert.Equal(t, first, second, "one container holds both members")
	})

	t.Run("php_two_namespace_blocks", func(t *testing.T) {
		const path = "pkg/rp.php"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "<?php\nnamespace App { class A {} }\nnamespace App { class B {} }\n",
		}})
		first := assertMemberAttaches(t, res, path, "pkg/rp.php:App.A", "namespace_definition")
		second := assertMemberAttaches(t, res, path, "pkg/rp.php:App.B", "namespace_definition")
		assert.NotEqual(t, first, second, "the two blocks are distinct containers")
	})

	t.Run("csharp_reopened_namespace", func(t *testing.T) {
		const path = "pkg/re.cs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "namespace App { class A {} }\nnamespace App { class B {} }\n",
		}})
		first := assertMemberAttaches(t, res, path, "pkg/re.cs:App.A", "namespace_declaration")
		second := assertMemberAttaches(t, res, path, "pkg/re.cs:App.B", "namespace_declaration")
		assert.NotEqual(t, first, second, "the two blocks are distinct containers")
	})
}

// usesTypeTargets returns the ToID of every USES_TYPE edge that survived
// resolution.
func usesTypeTargets(res PopulateResult) []string {
	var out []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeUsesType {
			out = append(out, e.ToId)
		}
	}
	return out
}

// nodeOnLine returns the ID of the single node of one of these kinds that
// starts on the given line. The fixtures below declare the TYPE SECOND, so
// naming the winner by its line is what tells the kind-aware rule apart from
// the accident of source order — a first-in-source tie-break would pick the
// other one.
func nodeOnLine(t *testing.T, res PopulateResult, path string, line int32, kinds ...string) string {
	t.Helper()
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	var found []string
	for _, n := range res.Nodes {
		if n.FilePath == path && want[n.Type] && n.StartLine == line {
			found = append(found, n.Id)
		}
	}
	require.Len(t, found, 1, "exactly one %v starts on line %d of %s", kinds, line, path)
	return found[0]
}

// TestCollidedTypeRefPrefersTheType proves a type reference naming a collided
// container resolves to the TYPE declaration rather than to an impl, namespace
// or companion-object block sharing the name — and abstains outright when the
// name is ambiguous, because a wrong edge is worse than a missing one.
//
// rust_uncollided_control is the known positive for every zero below: it runs
// the same measurement on a fixture with no collision and must come back
// non-empty, so an emitter that stopped producing USES_TYPE edges entirely
// cannot masquerade as correct abstention.
func TestCollidedTypeRefPrefersTheType(t *testing.T) {
	t.Run("rust_impl_first_struct_second", func(t *testing.T) {
		const path = "pkg/si.rs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "impl Thing { pub fn method(&self) {} }\npub struct Thing;\npub fn make() -> Thing { Thing }\n",
		}})
		structID := nodeOnLine(t, res, path, 2, "struct_item")
		implID := nodeOnLine(t, res, path, 1, "impl_item")

		targets := usesTypeTargets(res)
		assert.Len(t, targets, 3, "every reference to the collided name resolves")
		for _, to := range targets {
			assert.Equal(t, structID, to, "a type reference names the struct")
		}
		assert.NotContains(t, targets, implID, "the impl block is never a type's referent")
	})

	t.Run("scala_object_first_class_second", func(t *testing.T) {
		// A companion object beside its class is the second common shape of
		// this collision after Rust struct+impl.
		const path = "pkg/s.scala"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "object Shape { def make(): Shape = new Shape }\nclass Shape { def area(): Int = 1 }\n",
		}})
		classID := nodeOnLine(t, res, path, 2, "class_definition")
		objectID := nodeOnLine(t, res, path, 1, "object_definition")

		targets := usesTypeTargets(res)
		assert.Len(t, targets, 2)
		for _, to := range targets {
			assert.Equal(t, classID, to, "a type reference names the class")
		}
		assert.NotContains(t, targets, objectID, "the companion object is never a type's referent")
	})

	t.Run("rust_fn_and_struct_no_alias", func(t *testing.T) {
		// THE ABSTENTION CASE, and the only one here that discriminates in
		// EITHER declaration order. A function colliding with a type leaves TWO
		// candidates that could be the referent, so nothing is claimed and the
		// reference drops exactly as it does without the rule. Under a
		// deny-set-only design this fixture sent both edges to the function.
		const path = "pkg/g.rs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "pub fn Thing() {}\npub struct Thing {}\npub fn make() -> Thing { Thing{} }\n",
		}})
		assert.Empty(t, usesTypeTargets(res), "an ambiguous name claims nothing")
	})

	t.Run("rust_uncollided_control", func(t *testing.T) {
		// THE KNOWN POSITIVE. No collision, so the references resolve by the
		// ordinary path and this test's zeros keep their meaning.
		const path = "pkg/w.rs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "pub struct Widget;\npub fn build() -> Widget { Widget }\n",
		}})
		assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "pkg/w.rs:Widget", "pkg/w.rs:Widget"))
		assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "pkg/w.rs:build", "pkg/w.rs:Widget"))
		assert.Len(t, usesTypeTargets(res), 2)
	})

	t.Run("csharp_reopened_namespace_zero_survivors", func(t *testing.T) {
		// Both candidates for the name `App` are namespace declarations, so
		// ZERO survive the deny set and the rule abstains — an implementation
		// that claimed the alias when zero survive, rather than when exactly
		// one does, would fail the first half here.
		//
		// The four class-targeting edges are the within-fixture known positive
		// showing the first half is not passing on an empty edge set. They
		// resolve today with no aliasing at all and would resolve under any of
		// these rules; their surviving says nothing about abstention.
		//
		// What this fixture CANNOT discriminate is deny-set membership: a
		// same-kind pair tallies either 0 or 2 survivors, and both abstain.
		const path = "pkg/re.cs"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "namespace App { class A {} }\nnamespace App { class B {} }\n",
		}})
		first := nodeOnLine(t, res, path, 1, "namespace_declaration")
		second := nodeOnLine(t, res, path, 2, "namespace_declaration")

		targets := usesTypeTargets(res)
		assert.NotContains(t, targets, first, "an ambiguous namespace name claims nothing")
		assert.NotContains(t, targets, second, "an ambiguous namespace name claims nothing")
		assert.ElementsMatch(t, []string{
			"pkg/re.cs:App.A", "pkg/re.cs:App.A",
			"pkg/re.cs:App.B", "pkg/re.cs:App.B",
		}, targets, "the class-targeting references are unaffected")
	})

	t.Run("cpp_reopened_namespace_no_alias", func(t *testing.T) {
		// A NO-CHANGE SMOKE CASE, not a control: the C++ query emits no
		// USES_TYPE edge for this fixture at all, so its zero would hold under
		// any rule including no aliasing whatsoever. It is cheap evidence that
		// the C++ path gained nothing new, and it proves nothing about the deny
		// set.
		const path = "pkg/re.cpp"
		res := populateFixture(t, []fixtureFile{{
			path: path,
			src:  "namespace app { void a() {} }\nnamespace app { void b() {} }\n",
		}})
		assert.Empty(t, usesTypeTargets(res), "this fixture emits no type reference to begin with")
	})
}
