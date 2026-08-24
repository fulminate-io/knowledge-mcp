// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// boundRungModulePath is the module every Go fixture in this package imports
// through: a file under dir/ is imported as "example.com/fixture/dir". It is the
// same value populateFixture supplies, for the same reason — the Go resolver
// maps import paths onto repository directories and returns its zero result
// without one.
const boundRungModulePath = "example.com/fixture"

// boundRungHarness names WHICH of the three landed fixture harnesses a row
// drives. No row builds a selector of its own: the harnesses already encode the
// three different things a fixture can need, and picking among them by name is
// what keeps each row a statement about a RUNG rather than about plumbing.
type boundRungHarness int

const (
	// harnessPlain — chunk, fill binds against an EMPTY RepoContext, index. The
	// arms these rows exercise derive their answer from the source's own text.
	harnessPlain boundRungHarness = iota
	// harnessParented — harnessPlain narrowed to the reference a PARENTED
	// declaration emitted. A container chunk and its member chunk both walk the
	// member's body, so one source token yields two edges with the same target;
	// a rung that requires ref.Parent must pick the parented one explicitly, and
	// taking whichever the walk emitted first yields the copy that cannot reach
	// the rung at all.
	harnessParented
	// harnessOnDisk — the fixture is materialized into a temp tree and the
	// discovered file list is passed, which is what a MODULE RESOLVER needs to
	// turn a specifier into a file.
	harnessOnDisk
	// harnessOnDiskModule — harnessOnDisk plus the RepoContext's ModulePath, the
	// one extra fact the Go module resolver reads.
	harnessOnDiskModule
)

// boundRungRow is one bound-reachable rung's proof: a fixture, the referencing
// file, the edge type, the verbatim reference target, the rung expected to fire
// and the harness that can run it.
type boundRungRow struct {
	name     string
	rule     RefRule
	files    []fixtureFile
	file     string
	edgeType treesitter.EdgeType
	target   string
	harness  boundRungHarness
}

// resolve runs the row's fixture through the production ordering and returns the
// index plus the row's designated reference edge.
func (r boundRungRow) resolve(t *testing.T) (*declIndex, *treesitter.Edge) {
	t.Helper()
	switch r.harness {
	case harnessParented:
		return resolveFixtureParentedRef(t, r.files, r.file, r.edgeType, r.target)
	case harnessOnDisk:
		return resolveRepoFixtureRef(t, r.files, r.file, r.target)
	case harnessOnDiskModule:
		return resolveRepoFixtureRefInModule(t, r.files, r.file, r.target, boundRungModulePath)
	case harnessPlain:
		return resolveFixtureRef(t, r.files, r.file, r.edgeType, r.target)
	}
	t.Fatalf("row %q names no harness", r.name)
	return nil, nil
}

// boundRungRows carries ONE ROW PER BOUND-REACHABLE RUNG — nine of the twelve
// RefRule constants. The bound-reachable set is derived from the source rather
// than listed from memory: classify (resolve_walk.go) is the only producer of
// RefBound, and the three constants missing here can never reach it —
// RuleExternalQualifier and RuleNotDeclared are only ever returned alongside
// RefExternal, and RuleDynamicScope only alongside RefDynamic.
// TestRefRulePartitionIsTotal asserts that split holds as a partition.
var boundRungRows = []boundRungRow{
	{
		// C has no qualified call form, so its reference is BARE; the
		// one-definition rule is what makes the sibling translation unit's
		// same-named function a different scope rather than a second candidate.
		name: "own_scope_c", rule: RuleOwnScope,
		files: []fixtureFile{
			{path: "m/a.c", src: "int handle(void) { return 1; }\nint use(void) { return handle(); }\n"},
			{path: "m/b.c", src: "int handle(void) { return 2; }\n"},
		},
		file: "m/a.c", edgeType: treesitter.EdgeCalls, target: "handle",
		harness: harnessPlain,
	},
	{
		// The qualifier IS a declared container in the reference's own scope,
		// which is the rung's whole question — and B declares the same member, so
		// a rung that ignored the parent would reach a two-candidate group.
		name: "qualified_parent_python", rule: RuleQualifiedParent,
		files: []fixtureFile{
			{path: "bin/one.py", src: "class A:\n    def handle(self):\n        return 1\n\n\n" +
				"class B:\n    def handle(self):\n        return 2\n\n\ndef use():\n    return A.handle()\n"},
			{path: "bin/two.py", src: "import json\n\n\ndef main():\n    return json.dumps({'a': 1})\n"},
		},
		file: "bin/one.py", edgeType: treesitter.EdgeCalls, target: "A.handle",
		harness: harnessPlain,
	},
	{
		// THE ONE PARENTED ROW. The sibling rung requires ref.Parent, so the
		// unparented copy of this same token cannot reach it.
		name: "sibling_member_java", rule: RuleSiblingMember,
		files: []fixtureFile{
			{path: "m/A.java", src: "class A { int handle() { return 1; } int use() { return handle(); } }\n"},
			{path: "m/B.java", src: "class B { int other() { return 2; } }\n"},
		},
		file: "m/A.java", edgeType: treesitter.EdgeCalls, target: "handle",
		harness: harnessParented,
	},
	{
		// The qualifier is a VALUE whose declared type the chunker recorded per
		// declaration, so the rung looks Server.Handle up rather than asking
		// whether `s` is a declared parent.
		name: "typed_qualifier_go", rule: RuleTypedQualifier,
		files: []fixtureFile{
			{path: "svc/t.go", src: "package svc\n\ntype Server struct{}\n\nfunc (s *Server) Handle() int { return 1 }\n"},
			{path: "svc/c.go", src: "package svc\n\nfunc run(s Server) int { return s.Handle() }\n"},
		},
		file: "svc/c.go", edgeType: treesitter.EdgeCalls, target: "s.Handle",
		harness: harnessPlain,
	},
	{
		// A fully-qualified name carries no import statement for any arm to have
		// bound, so this rung derives the scope from the qualifier itself.
		name: "qualified_path_java", rule: RuleQualifiedPath,
		files: []fixtureFile{
			{path: "com/acme/foo/Bar.java", src: "package com.acme.foo;\n\nclass Bar { void go() {} }\n"},
			{path: "app/Main.java", src: "class Main { com.acme.foo.Bar field; }\n"},
		},
		file: "app/Main.java", edgeType: treesitter.EdgeUsesType, target: "com.acme.foo.Bar",
		harness: harnessPlain,
	},
	{
		name: "qualified_import_go", rule: RuleQualifiedImport,
		files: []fixtureFile{
			{path: "b/b.go", src: "package b\n\nfunc Helper() int { return 1 }\n"},
			{path: "a/a.go", src: "package a\n\nimport \"example.com/fixture/b\"\n\nfunc Use() int { return b.Helper() }\n"},
		},
		file: "a/a.go", edgeType: treesitter.EdgeCalls, target: "b.Helper",
		harness: harnessOnDiskModule,
	},
	{
		// A DOT IMPORT folds another scope into this file's namespace wholesale,
		// so the reference is unqualified and the rung records only WHERE the one
		// answer came from.
		name: "dot_scope_go", rule: RuleDotScope,
		files: []fixtureFile{
			{path: "b/b.go", src: "package b\n\nfunc Helper() int { return 1 }\n"},
			{path: "a/a.go", src: "package a\n\nimport . \"example.com/fixture/b\"\n\nfunc Use() int { return Helper() }\n"},
		},
		file: "a/a.go", edgeType: treesitter.EdgeCalls, target: "Helper",
		harness: harnessOnDiskModule,
	},
	{
		name: "unqualified_import_ts", rule: RuleUnqualifiedImport,
		files: []fixtureFile{
			{path: "e2e/one.ts", src: "export function setup(): number {\n  return 3;\n}\n"},
			{path: "e2e/two.ts", src: "" +
				"import { setup } from './one';\n\nexport function run(): number {\n  return setup();\n}\n"},
		},
		file: "e2e/two.ts", edgeType: treesitter.EdgeCalls, target: "setup",
		harness: harnessOnDisk,
	},
	{
		// The member is indexed with its container as Parent, so the top-level
		// lookup misses and the PARENT-KEYED one answers.
		name: "qualified_member_ts", rule: RuleQualifiedMember,
		files: []fixtureFile{
			{path: "e2e/one.ts", src: "" +
				"export class Foo {\n  static method(): number {\n    return 1;\n  }\n}\n"},
			{path: "e2e/two.ts", src: "" +
				"import { Foo } from './one';\n\nexport function run(): number {\n  return Foo.method();\n}\n"},
		},
		file: "e2e/two.ts", edgeType: treesitter.EdgeCalls, target: "Foo.method",
		harness: harnessOnDisk,
	},
}

// TestBoundEdgeCarriesResolvingRung is the per-rung proof that a BOUND edge
// names the rung that bound it, for every rung that can produce one.
//
// IT DRIVES THE PRODUCTION EMITTER, never a re-implementation of it: the row
// resolves its reference through the production ordering and then hands the
// edge to resolveReference, so what is asserted is the edge a collect would
// actually write.
//
// EACH ROW CARRIES TWO CONTROLS AHEAD OF THE METHOD ASSERTION — the status must
// be RefBound and the rule must be the row's own. Without them a fixture that
// stopped producing its rung would satisfy the Method assertion vacuously by
// producing no bound edge at all, or would satisfy it under a DIFFERENT rung.
func TestBoundEdgeCarriesResolvingRung(t *testing.T) {
	for _, row := range boundRungRows {
		t.Run(row.name, func(t *testing.T) {
			ix, e := row.resolve(t)
			require.NotNil(t, e.Ref, "the fixture emitted no reference site to resolve")

			got := resolveRef(ix, e.Ref, e.ToID)
			require.Equal(t, RefBound, got.Status,
				"control: %s must BIND, or the Method assertion below proves nothing", row.name)
			require.Equal(t, row.rule, got.Rule,
				"control: %s must bind through its own rung", row.name)

			var stats resolveStats
			out := resolveReference(e, ix, map[string]bool{e.FromID: true}, &stats, make(groupOrdinals))
			require.Len(t, out, 1, "a bound reference emits exactly one edge")

			assert.Equal(t, string(row.rule), out[0].Method,
				"the bound edge must carry the resolving rung")
			// THE "METHOD ONLY" HALF. Confidence and Evidence are the residue
			// fields a GROUP carries; a bound edge gains the attribution and
			// nothing else, so a change that started stamping either of them
			// here reds this row rather than passing as a superset.
			assert.Empty(t, out[0].Evidence, "a bound edge carries no group key")
			assert.Zero(t, out[0].Confidence, "a bound edge carries no split confidence")
		})
	}
}

// TestGroupEdgesKeepGroupKindMethod is the KNOWN-NEGATIVE half of the rung
// stamp: on a GROUP, Method names the group KIND and must never be replaced by
// the rung that produced the group.
//
// THIS IS A CHARACTERIZATION GUARD, NOT A RED-FIRST TEST. It is green before the
// bound-edge stamp exists and green after it, and that is exactly its job:
// without it the positive half above is satisfied by a stamp applied to every
// emission arm indiscriminately, which would overwrite both group kinds and lose
// the closed-versus-open distinction the group Method carries.
func TestGroupEdgesKeepGroupKindMethod(t *testing.T) {
	rows := []struct {
		name       string
		files      []fixtureFile
		file       string
		target     string
		rule       RefRule
		method     string
		candidates int
	}{
		{
			// An OPEN set: the qualifier is a value, so the language dispatches
			// at runtime and the referent may be beyond static reach.
			name: "ruby_dynamic_group",
			files: []fixtureFile{
				{path: "m/a.rb", src: "class A\n  def handle\n    1\n  end\nend\n\n" +
					"class B\n  def handle\n    2\n  end\nend\n\ndef use\n  obj.handle()\nend\n"},
				{path: "m/b.rb", src: "def handle\n  2\nend\n"},
			},
			file: "m/a.rb", target: "obj.handle",
			rule: RuleDynamicScope, method: kgtypes.EdgeMethodDynamic, candidates: 2,
		},
		{
			// A CLOSED set: bash has no qualified call form at all, so the two
			// same-named functions in one file resolve through the own-scope rung
			// to a genuinely ambiguous pair — a bound-reachable RUNG producing a
			// group, which is what makes this row the sharper of the two.
			name: "bash_ambiguous_group",
			files: []fixtureFile{
				{path: "scripts/a.test.sh", src: "set -euo pipefail\n\nfail() {\n  echo \"a failed\" >&2\n}\n\n" +
					"fail() {\n  echo \"b failed differently\" >&2\n}\n\nrun() {\n  fail\n}\n"},
				{path: "scripts/b.test.sh", src: "set -euo pipefail\n\nfail() {\n  echo \"b failed differently\" >&2\n}\n"},
			},
			file: "scripts/a.test.sh", target: "fail",
			rule: RuleOwnScope, method: kgtypes.EdgeMethodAmbiguousName, candidates: 2,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ix, e := resolveFixtureRef(t, row.files, row.file, treesitter.EdgeCalls, row.target)
			require.NotNil(t, e.Ref, "the fixture emitted no reference site to resolve")

			got := resolveRef(ix, e.Ref, e.ToID)
			require.Equal(t, row.rule, got.Rule, "control: the row's own rung must fire")
			require.Len(t, got.Candidates, row.candidates,
				"control: a group needs more than one candidate to be a group")

			var stats resolveStats
			out := resolveReference(e, ix, map[string]bool{e.FromID: true}, &stats, make(groupOrdinals))
			require.Len(t, out, row.candidates, "one edge per candidate, never a narrowed guess")

			for _, ge := range out {
				assert.Equal(t, row.method, ge.Method, "a group member carries the GROUP KIND")
				assert.NotEqual(t, string(got.Rule), ge.Method,
					"a group member must never carry the rung that produced it")
				assert.NotEmpty(t, ge.Evidence, "every group member carries the group key")
			}
		})
	}
}

// boundReachableRules is the set of rungs that can produce a BOUND edge, and it
// is the same set TestBoundEdgeCarriesResolvingRung tables one row each for.
var boundReachableRules = []RefRule{
	RuleQualifiedImport,
	RuleQualifiedMember,
	RuleQualifiedParent,
	RuleQualifiedPath,
	RuleTypedQualifier,
	RuleUnqualifiedImport,
	RuleSiblingMember,
	RuleOwnScope,
	RuleDotScope,
}

// neverBoundRules is the complement: the rungs only ever returned alongside a
// non-bound status.
var neverBoundRules = []RefRule{
	RuleExternalQualifier,
	RuleDynamicScope,
	// The rung returns RefExternal with NO candidates at all, so it can never
	// stamp a bound edge — which is why it belongs here rather than in
	// boundReachableRules.
	RuleDynamicRungSkipped,
	RuleNotDeclared,
}

// TestRefRulePartitionIsTotal keeps the per-rung table HONEST ABOUT ITS OWN
// COVERAGE. The table above proves nine rungs stamp; this test proves nine is
// all of them that can, by asserting the two sets partition the whole RefRule
// vocabulary.
//
// IT IS HALF OF A TWO-MEASUREMENT GATE. This half counts the constants the two
// sets name; its criterion counts the const block in the source independently.
// A fourteenth constant landing with no cell fails one of the two, so neither
// measurement can quietly redefine the vocabulary to match itself.
func TestRefRulePartitionIsTotal(t *testing.T) {
	seen := map[RefRule]string{}
	for _, r := range boundReachableRules {
		require.NotContains(t, seen, r, "%q is listed twice in boundReachableRules", r)
		seen[r] = "bound-reachable"
	}
	for _, r := range neverBoundRules {
		require.NotContains(t, seen, r,
			"%q is in BOTH sets, so the partition is not disjoint", r)
		seen[r] = "never-bound"
	}
	assert.Len(t, seen, 13,
		"the two sets must cover the whole RefRule vocabulary exactly once each")

	bound := map[RefRule]bool{}
	for _, r := range boundReachableRules {
		bound[r] = true
	}
	for _, row := range boundRungRows {
		assert.True(t, bound[row.rule],
			"row %q names %q, which is not a bound-reachable rung", row.name, row.rule)
	}
	assert.Len(t, boundRungRows, len(boundReachableRules),
		"every bound-reachable rung needs exactly one row")
}
