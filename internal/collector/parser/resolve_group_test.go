// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// groupEdgesOf returns every emitted edge carrying the given Method, in
// emission order.
func groupEdgesOf(res PopulateResult, method string) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if e.Method == method {
			out = append(out, e)
		}
	}
	return out
}

// evidenceKeys returns the distinct Evidence values across a set of edges.
func evidenceKeys(edges []*knowledgev1.Edge) map[string]int {
	keys := map[string]int{}
	for _, e := range edges {
		keys[e.Evidence]++
	}
	return keys
}

// twoHandlers declares one name twice in ONE Go package, with DIFFERENT bodies
// so a fixture that collapsed the pair into a single declaration is
// distinguishable from a correct two-way bind, plus a caller of it.
var twoHandlers = []fixtureFile{
	{path: "svc/alpha.go", src: "" +
		"package svc\n\nfunc Handle() string {\n\treturn \"the alpha implementation of handle\"\n}\n"},
	{path: "svc/beta.go", src: "" +
		"package svc\n\nfunc Handle() string {\n\treturn \"a beta implementation with a different body\"\n}\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\nfunc Caller() string {\n\treturn Handle()\n}\n"},
}

// TestAmbiguousReferenceMultiBinds pins the CLOSED group: a reference that
// binds to more than one surviving declaration emits one edge PER CANDIDATE,
// never a narrowed guess.
//
// NAMED CATCHER: an implementation that wires Method but not Confidence, or
// Confidence but not Method. Both are asserted on every member.
func TestAmbiguousReferenceMultiBinds(t *testing.T) {
	require.NotEqual(t, twoHandlers[0].src, twoHandlers[1].src,
		"the two candidates must have different content, so a collapsed fixture is distinguishable")

	res := populateFixture(t, twoHandlers)

	got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
	require.Len(t, got, 2, "one edge per candidate; the replaced scalar map could only ever emit one")

	for _, e := range got {
		assert.Equal(t, "svc/caller.go:Caller", e.FromId)
		assert.Equal(t, string(kgtypes.EdgeCalls), e.Type)
		assert.InDelta(t, 0.5, e.Confidence, 1e-9, "Confidence is 1/N")
		assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
	}
	assert.Equal(t, []string{"svc/alpha.go:Handle", "svc/beta.go:Handle"},
		[]string{got[0].ToId, got[1].ToId}, "candidates arrive in file order")

	keys := evidenceKeys(got)
	require.Len(t, keys, 1, "both members of one group share one key")
	for k := range keys {
		assert.NotEmpty(t, k)
	}
}

// TestDynamicReferenceEmitsOpenSetGroup pins the OPEN group. It is the catcher
// kgtypes.EdgeMethodDynamic needed: the constant had no emitter before the
// open-set ruling, and a carrier arrives with its consumer or not at all.
//
// THE FIXTURES ARE GO, on the plan-time corpus measurement rather than on a
// rule about callee shape: go produced 11,917 dynamic groups and bash produced
// none, so a bash fixture here would very likely be a vacuous catcher.
func TestDynamicReferenceEmitsOpenSetGroup(t *testing.T) {
	// A qualified call on a VALUE whose method is NOT an indexed declaration, so
	// the typed rung cannot bind it and the language dispatches it at runtime.
	//
	// THE METHOD IS PROMOTED FROM OUTSIDE THE INDEXED SCOPE, and that choice is
	// load-bearing rather than incidental. An IN-REPO interface used to be the
	// natural example here, but an interface's method specs are now chunked and
	// indexed under Parent=<Interface>, so such a fixture binds through the typed
	// rung and produces no dynamic group at all — it would silently stop being a
	// dispatch fixture. A method promoted through an embed of an unindexed type
	// is undecidable by construction and stays that way.
	dispatchCaller := fixtureFile{path: "svc/invoke.go", src: "" +
		"package svc\n\nimport \"example.com/ext\"\n\ntype holder struct{ ext.Base }\n\n" +
		"func Invoke(x holder) int {\n\treturn x.Run()\n}\n"}

	t.Run("emits_open_set_group", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{
			{path: "svc/runner.go", src: "" +
				"package svc\n\ntype Runner struct{ n int }\n\nfunc (r Runner) Run() int {\n\treturn r.n\n}\n"},
			{path: "svc/free.go", src: "" +
				"package svc\n\nfunc Run() int {\n\treturn 7\n}\n"},
			dispatchCaller,
		})

		got := groupEdgesOf(res, kgtypes.EdgeMethodDynamic)
		require.Len(t, got, 2, "both declarations of the name in this scope are dispatch candidates")
		for _, e := range got {
			assert.InDelta(t, 0.5, e.Confidence, 1e-9)
			assert.Equal(t, kgtypes.EdgeMethodDynamic, e.Method)
		}
		require.Len(t, evidenceKeys(got), 1, "one reference, one key")
	})

	t.Run("zero_candidates_emit_nothing", func(t *testing.T) {
		// The same dispatch with NO declaration of the name anywhere in the
		// scope. An empty group would represent nothing, so none is created.
		res := populateFixture(t, []fixtureFile{dispatchCaller})
		assert.Empty(t, groupEdgesOf(res, kgtypes.EdgeMethodDynamic),
			"a dispatch whose scope declares nothing by that name emits no edge")

		// The outcome is still DYNAMIC, not External: the ladder stopped at R3.
		ix := indexOf(t)
		res2 := resolveRef(ix, refSiteFor("svc/invoke.go", "dir:svc", ""), "x.Run")
		assert.Equal(t, RefDynamic, res2.Status)
		assert.Equal(t, RuleDynamicScope, res2.Rule)
		assert.Empty(t, res2.Candidates)
	})

	t.Run("method_tags_are_distinct", func(t *testing.T) {
		// ONE fixture producing BOTH kinds. Also the catcher for a botched
		// constant move: if one emission site kept a package-local twin while
		// the other moved to kgtypes, the two values diverge and this fails.
		files := append(append([]fixtureFile{}, twoHandlers...),
			fixtureFile{path: "svc/runner.go", src: "" +
				"package svc\n\ntype Runner struct{ n int }\n\nfunc (r Runner) Run() int {\n\treturn r.n\n}\n"},
			dispatchCaller,
		)
		res := populateFixture(t, files)

		ambiguous := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		dynamic := groupEdgesOf(res, kgtypes.EdgeMethodDynamic)
		require.NotEmpty(t, ambiguous, "control: the fixture must produce a CLOSED group")
		require.NotEmpty(t, dynamic, "control: the fixture must produce an OPEN group")

		require.NotEqual(t, kgtypes.EdgeMethodAmbiguousName, kgtypes.EdgeMethodDynamic,
			"the two kinds must be distinguishable on the wire")
		assert.Equal(t, "ambiguous-name", kgtypes.EdgeMethodAmbiguousName)
		assert.Equal(t, "dynamic", kgtypes.EdgeMethodDynamic)
	})
}

// twoAmbiguousNames declares TWO names twice each in one Go package, and calls
// both from ONE declaration — so the two references share a file, a RefByte and
// an edge type, and differ only in the target text.
//
// UniqueOnce is the KNOWN-POSITIVE CONTROL for every subtest below: it is
// declared exactly once, so its call BINDS and its edge must carry an EMPTY
// Evidence. Without it, a build that never populated Evidence at all would
// satisfy "distinct" and "stable" perfectly.
var twoAmbiguousNames = []fixtureFile{
	{path: "svc/one.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 1 }\n\nfunc Beta() int { return 2 }\n"},
	{path: "svc/two.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 100 }\n\nfunc Beta() int { return 200 }\n"},
	{path: "svc/unique.go", src: "" +
		"package svc\n\nfunc UniqueOnce() int { return 3 }\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\nfunc Caller() int {\n\treturn Alpha() + Beta() + UniqueOnce()\n}\n"},
}

// TestAmbiguousGroupKeyIsStableAndExactlyOneOf pins the group key: shared by
// every member of one group, distinct per reference AND per edge type, stable
// across runs, and EMPTY on a bound edge.
func TestAmbiguousGroupKeyIsStableAndExactlyOneOf(t *testing.T) {
	// The control, asserted once for the whole test: a bound edge carries no
	// group key, because it is not one of several guesses.
	t.Run("bound_edge_has_no_key", func(t *testing.T) {
		res := populateFixture(t, twoAmbiguousNames)
		var bound int
		for _, e := range res.Edges {
			if e.ToId == "svc/unique.go:UniqueOnce" && kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
				bound++
				assert.Empty(t, e.Evidence, "a bound edge carries no group key")
				assert.Equal(t, string(RuleOwnScope), e.Method,
					"a bound edge carries the rung that resolved it")
			}
		}
		require.Equal(t, 1, bound, "control: the singly-declared callee must bind to exactly one edge")
	})

	t.Run("shared_key", func(t *testing.T) {
		res := populateFixture(t, twoHandlers)
		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.Len(t, got, 2)
		require.Len(t, evidenceKeys(got), 1, "every edge of one reference carries the same key")
	})

	t.Run("distinct_per_reference", func(t *testing.T) {
		res := populateFixture(t, twoAmbiguousNames)
		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		// Two references, two candidates each.
		require.Len(t, got, 4)
		require.Len(t, evidenceKeys(got), 2,
			"two references from ONE declaration must not collide onto one key")
	})

	t.Run("distinct_per_edge_type", func(t *testing.T) {
		// THE EDGE-TYPE CATCHER, on a shape that can exist in real source: a
		// directory may hold `package x` AND its external `package x_test`, and
		// both land in the same dir scope, so Foo is genuinely ambiguous there.
		// The embedding declaration emits BOTH USES_TYPE and EMBEDS to the same
		// verbatim target from the same declaration — same file, same RefByte.
		res := populateFixture(t, []fixtureFile{
			{path: "x/foo.go", src: "package x\n\ntype Foo struct{ a int }\n"},
			{path: "x/foo_ext_test.go", src: "package x_test\n\ntype Foo struct{ b string }\n"},
			{path: "x/embed.go", src: "package x\n\ntype S struct {\n\tFoo\n}\n"},
		})

		// Scoped to the ONE declaration that emits both edge types. The other
		// two files each emit a USES_TYPE to Foo of their own, and those are
		// different references that SHOULD have different keys — counting them
		// here would test reference-distinctness again instead of type-distinctness.
		byType := map[string]map[string]bool{}
		for _, e := range groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName) {
			if e.FromId != "x/embed.go:S" {
				continue
			}
			if byType[e.Type] == nil {
				byType[e.Type] = map[string]bool{}
			}
			byType[e.Type][e.Evidence] = true
		}
		require.Len(t, byType, 2,
			"the fixture must produce groups of TWO edge types, else the catcher is vacuous: got %v", byType)

		seen := map[string]bool{}
		for edgeType, keys := range byType {
			require.Len(t, keys, 1, "one reference of one type is one group (%s)", edgeType)
			for k := range keys {
				require.False(t, seen[k],
					"two edge types from ONE reference collided onto one key: %s", k)
				seen[k] = true
			}
		}
	})

	t.Run("stable_across_runs", func(t *testing.T) {
		triples := func(res PopulateResult) []string {
			var out []string
			for _, e := range res.Edges {
				if e.Evidence != "" {
					out = append(out, e.FromId+"|"+e.ToId+"|"+e.Evidence)
				}
			}
			return out
		}
		first := triples(populateFixture(t, twoAmbiguousNames))
		second := triples(populateFixture(t, twoAmbiguousNames))
		require.NotEmpty(t, first, "control: the fixture must produce keyed edges at all")
		assert.Equal(t, first, second, "the key is deterministic across collects of an unchanged file")
	})

	t.Run("key_shape", func(t *testing.T) {
		res := populateFixture(t, twoAmbiguousNames)
		// verbatim target + ':' + edge type + ':' + enclosing declaration id +
		// ':' + within-file ordinal. NO BYTE OFFSET, NO LINE, NO COLUMN — the
		// shape this regex used to pin (`svc/caller.go:<digits>:CALLS:<target>`)
		// was the position-derived key whose re-stamping on every unrelated edit
		// orphaned a row per site.
		shape := regexp.MustCompile(`^(Alpha|Beta):CALLS:svc/caller\.go:[A-Za-z0-9_.]+:\d+$`)
		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.NotEmpty(t, got)
		for _, e := range got {
			assert.Regexp(t, shape, e.Evidence,
				"key is target + ':' + edge type + ':' + enclosing declaration id + ':' + ordinal")
		}
	})
}
