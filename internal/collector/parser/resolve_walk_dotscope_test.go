// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestResolveRef_NoDotScopesIsUnchanged is a CHARACTERIZATION GUARD of the
// dot-scope work. Its job is to FAIL IF A FUTURE EDIT MOVES THE ZERO-DOT-SCOPE
// PATH — the path every reference in the measured corpus takes — not to prove
// that ticket's new behavior, and it was green on both sides of it.
//
// ONE OF ITS ROWS WAS DELIBERATELY MOVED by the per-language sibling-rung
// gating (ful1344), and the move is recorded here so a later reader does not
// read it as drift. The guard used to pin a GO site binding a sibling member;
// Go now SKIPS the sibling rung, because a bare receiverless call in a Go method
// is `undefined: a` at the compiler and never a call on the receiver. The row
// was not deleted and its expectation was not merely relaxed — the guard now
// pins BOTH sides of that decision: a java site (java keeps the rung, executed)
// binding its sibling, and the same shape in Go landing not-declared.
//
// IT ASSERTS THE COMPLETE SET OF (from, to, rule) TRIPLES rather than a count,
// because a count still passes when two edges swap targets. Four outcomes the
// gathered rung sits beside are pinned: a name bound in the own scope, a name
// bound to a sibling member in a language that keeps that rung, the same shape
// in a language that skips it, and a name declared nowhere.
//
// WHAT A PASSING SUITE CANNOT ANSWER, stated because it is the honest limit:
// these fixtures prove the mechanism on constructed sites. No live-corpus row
// exercises the new rung, because the corpus measures ZERO dot imports. The
// construct census in the binds pass is what would make a future corpus's dot
// imports visible; until one appears, the fixtures are the whole proof.
func TestResolveRef_NoDotScopesIsUnchanged(t *testing.T) {
	ix := indexOf(t,
		&declRec{NodeID: "svc/a.go:Free", File: "svc/a.go", Scope: "dir:svc", Name: "Free"},
		// THE GO SIBLING DECLARATION IS STILL INDEXED, and that is what makes
		// the gated row below a statement about the RUNG rather than about a
		// missing node: the declaration the reference would have bound is
		// present and reachable, and the reference still does not reach it.
		&declRec{NodeID: "svc/t.go:Thing.Do", File: "svc/t.go", Scope: "dir:svc", Parent: "Thing", Name: "Do"},
		// The keeping-language twin, in its OWN scope so the two cannot collide
		// into one ambiguous candidate set under a single key. THE SCOPE IS A
		// NAMESPACE ONE because java resolves at package scope — a `file:` scope
		// is a string no java declaration is ever indexed under, and a synthetic
		// fixture carrying one is a signpost pointing at a scope model the
		// language no longer has.
		&declRec{NodeID: "svc/t.java:Thing.Do", File: "svc/t.java", Scope: "ns:java:svc_pkg", Parent: "Thing", Name: "Do"},
		// Declared, but in a scope these references cannot see: the catcher for
		// a ladder that searches by name rather than within one scope unit.
		&declRec{NodeID: "other/a.go:Missing", File: "other/a.go", Scope: "dir:other", Name: "Missing"},
	)

	// NO DOT SCOPES ANYWHERE — refSiteFor leaves the map nil, which is exactly
	// what the chunker produces for a language with no arm.
	topLevel := refSiteFor("svc/c.go", "dir:svc", "")
	inThing := refSiteFor("svc/t.go", "dir:svc", "Thing")
	inThingJava := refSiteForLang("svc/t.java", "ns:java:svc_pkg", "Thing", treesitter.LangJava)
	require.Nil(t, topLevel.DotScopes, "the guarded path is the NIL-map path")
	require.Nil(t, inThing.DotScopes)
	require.Nil(t, inThingJava.DotScopes)

	// CONTROL FOR THE GATED ROW: the Go sibling declaration is genuinely
	// reachable — a QUALIFIED reference from the same site binds it. Without
	// this, the not-declared row below would pass just as well against an index
	// that never held svc/t.go:Thing.Do at all.
	reachable := resolveRef(ix, inThing, "Thing.Do")
	require.Equal(t, RefBound, reachable.Status)
	require.Equal(t, RuleQualifiedParent, reachable.Rule)
	require.Len(t, reachable.Candidates, 1)
	require.Equal(t, "svc/t.go:Thing.Do", reachable.Candidates[0].NodeID,
		"control: the gated row's target is indexed and reachable by another rung")

	type triple struct {
		from, to string
		rule     RefRule
	}
	cases := []struct {
		from, target string
		ref          *treesitter.RefSite
	}{
		{from: "svc/c.go:Caller", target: "Free", ref: topLevel},
		{from: "svc/t.java:Thing.Other", target: "Do", ref: inThingJava},
		{from: "svc/t.go:Thing.Other", target: "Do", ref: inThing},
		{from: "svc/c.go:Caller", target: "Missing", ref: topLevel},
	}

	got := make([]triple, 0, len(cases))
	for _, c := range cases {
		res := resolveRef(ix, c.ref, c.target)
		require.NotEmpty(t, string(res.Rule), "every return path names the rule that produced it")
		to := ""
		if len(res.Candidates) == 1 {
			to = res.Candidates[0].NodeID
		}
		got = append(got, triple{from: c.from, to: to, rule: res.Rule})
	}

	assert.Equal(t, []triple{
		{from: "svc/c.go:Caller", to: "svc/a.go:Free", rule: RuleOwnScope},
		// java KEEPS the sibling rung — executed: the bare call compiles and
		// runs on the implicit this.
		{from: "svc/t.java:Thing.Other", to: "svc/t.java:Thing.Do", rule: RuleSiblingMember},
		// go SKIPS it — executed: `./x.go:7:30: undefined: a`. The declaration
		// is indexed and reachable (see the control above), so this row is the
		// gate and not a gap.
		{from: "svc/t.go:Thing.Other", to: "", rule: RuleNotDeclared},
		{from: "svc/c.go:Caller", to: "", rule: RuleNotDeclared},
	}, got, "the zero-dot-scope path resolves exactly as it did before the gather existed, "+
		"except for the one row the per-language sibling gate moved")

	// THE SAME THREE REFERENCES THROUGH THE EMITTER, so the guard covers the
	// edges a collect actually writes and not only the ladder's verdict.
	results := []*treesitter.Result{{
		FilePath: "svc/c.go", Language: treesitter.LangGo,
		Edges: []treesitter.Edge{
			refEdge("svc/c.go:Caller", "Free", topLevel),
			refEdge("svc/t.java:Thing.Other", "Do", inThingJava),
			refEdge("svc/t.go:Thing.Other", "Do", inThing),
			refEdge("svc/c.go:Caller", "Missing", topLevel),
		},
	}}
	nodeIDs := map[string]bool{
		"svc/c.go:Caller": true, "svc/t.go:Thing.Other": true, "svc/t.java:Thing.Other": true,
		"svc/a.go:Free": true, "svc/t.go:Thing.Do": true, "svc/t.java:Thing.Do": true,
		"other/a.go:Missing": true,
	}
	edges, stats := resolveEdgesWithStats(results, ix, nodeIDs)

	// KNOWN-POSITIVE CONTROL: without it, a fixture that resolved nothing at
	// all would satisfy every equality below.
	require.NotEmpty(t, edges, "control: the fixture must produce edges before their contents mean anything")
	require.Len(t, edges, 2,
		"two references resolve — the own-scope name and the keeping-language sibling; "+
			"the undeclared name and the GATED go sibling each emit nothing")
	assert.Equal(t, "svc/a.go:Free", edges[0].ToId)
	assert.Equal(t, "svc/t.java:Thing.Do", edges[1].ToId)
	assert.Equal(t, 0, stats.DotScopeBinds, "no dot scopes anywhere means no dot-scope residue")
	assert.Equal(t, 0, stats.DotScopeGroups)
	assert.Equal(t, 2, stats.Bound, "the known-positive for the two zeros above")
}

// dotSiteFor builds a reference site carrying a set of dot scopes — the shape
// the chunker allocates and the binds pass fills for a file with dot imports.
func dotSiteFor(file, scope string, dotScopes ...string) *treesitter.RefSite {
	ref := refSiteFor(file, scope, "")
	ref.DotScopes = map[string]bool{}
	for _, s := range dotScopes {
		ref.DotScopes[s] = true
	}
	return ref
}

// refEdge builds one CALLS edge from a caller declaration to a verbatim target,
// carrying the given site — the carrier the chunker would have emitted.
func refEdge(from, target string, ref *treesitter.RefSite) treesitter.Edge {
	return treesitter.Edge{
		FromID: from,
		ToID:   target,
		Type:   treesitter.EdgeCalls,
		Weight: 1,
		Ref:    ref,
	}
}

// TestResolveRef_DotScopes proves the four outcomes of the gathered unqualified
// rung: an exact cross-scope bind, and three shapes of closed ambiguous group.
//
// NONE OF THESE IS A SHADOWING ROW, and the distinction is the whole design.
// A shadowing row would assert a WINNER between two declarations — which Go
// does not pose, because at package level it FORBIDS the collision rather than
// resolving it. Every multi-hit row below asserts a GROUP whose members carry
// equal Confidence. Where a row pins ORDER it is pinning the byte-identical
// edge SEQUENCE a group needs across collects, NOT a precedence.
func TestResolveRef_DotScopes(t *testing.T) {
	t.Run("cross_scope_bind", func(t *testing.T) {
		// THE ONLY ROW HERE THAT IS LEGAL GO, and the ticket's whole purpose:
		// the file's own scope declares no Foo, one dot-imported scope does,
		// and the unqualified reference binds exactly across the boundary.
		ix := indexOf(t,
			&declRec{NodeID: "lib/lib.go:Foo", File: "lib/lib.go", Scope: "dir:lib", Name: "Foo"},
			&declRec{NodeID: "app/own.go:Local", File: "app/own.go", Scope: "dir:app", Name: "Local"},
		)
		ref := dotSiteFor("app/main.go", "dir:app", "dir:lib")

		got := resolveRef(ix, ref, "Foo")
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleDotScope, got.Rule)
		assert.Equal(t, "dot-scope", string(got.Rule),
			"the rule's recorded VALUE is what an audit reads")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "lib/lib.go:Foo", got.Candidates[0].NodeID,
			"a dot import binds the reference into the DOTTED scope, not the caller's own")

		// The emitted edge is a plain bound edge: one edge, carrying the RUNG
		// that resolved it on Method and none of the residue fields, because a
		// bound reference is not one of several guesses.
		results := []*treesitter.Result{{
			FilePath: "app/main.go", Language: treesitter.LangGo,
			Edges: []treesitter.Edge{refEdge("app/main.go:Run", "Foo", ref)},
		}}
		nodeIDs := map[string]bool{"app/main.go:Run": true, "lib/lib.go:Foo": true, "app/own.go:Local": true}
		edges := resolveEdges(results, ix, nodeIDs)
		require.Len(t, edges, 1, "an exact cross-scope bind emits exactly one edge")
		assert.Equal(t, "lib/lib.go:Foo", edges[0].ToId)
		assert.Equal(t, string(RuleDotScope), edges[0].Method,
			"a bound edge carries the rung that resolved it")

		// KNOWN-POSITIVE CONTROL: the same site resolves a name its OWN scope
		// declares, so the index and the site are live and the bind above is
		// not an empty fixture agreeing with everything.
		ctrl := resolveRef(ix, ref, "Local")
		assert.Equal(t, RefBound, ctrl.Status)
		assert.Equal(t, RuleOwnScope, ctrl.Rule,
			"a name the own scope declares still binds under the own-scope rule")
	})

	t.Run("own_and_dot_multi_hit", func(t *testing.T) {
		// THIS PROGRAM DOES NOT COMPILE, verified at the pinned toolchain:
		// a local Foo alongside a dot import of a package exporting Foo is
		// "Foo already declared through dot-import of package q1". It is a
		// DECLARATION-time error, so it holds whether or not Foo is ever
		// referenced, and it holds even when the local Foo is in a DIFFERENT
		// FILE of the same package.
		//
		// THE COLLECTOR REACHES THE SHAPE ANYWAY, which is why the row exists:
		// it parses every file regardless of build tags, so it sees a UNION of
		// build configurations no single build has. The honest report is a
		// group — "these declarations disagree and no single build has both" —
		// not a winner. Do not delete this row as invalid Go; it carries the
		// same standing as the build-tag-variant rows elsewhere in this suite.
		ix := indexOf(t,
			&declRec{NodeID: "app/own.go:Foo", File: "app/own.go", Scope: "dir:app", Name: "Foo"},
			&declRec{NodeID: "lib/lib.go:Foo", File: "lib/lib.go", Scope: "dir:lib", Name: "Foo"},
			&declRec{NodeID: "app/own.go:Local", File: "app/own.go", Scope: "dir:app", Name: "Local"},
		)
		ref := dotSiteFor("app/main.go", "dir:app", "dir:lib")

		got := resolveRef(ix, ref, "Foo")
		assert.Equal(t, RefAmbiguous, got.Status)
		assert.Equal(t, RuleDotScope, got.Rule)
		require.Len(t, got.Candidates, 2)

		results := []*treesitter.Result{{
			FilePath: "app/main.go", Language: treesitter.LangGo,
			Edges: []treesitter.Edge{refEdge("app/main.go:Run", "Foo", ref)},
		}}
		nodeIDs := map[string]bool{
			"app/main.go:Run": true, "app/own.go:Foo": true,
			"lib/lib.go:Foo": true, "app/own.go:Local": true,
		}
		edges := resolveEdges(results, ix, nodeIDs)

		require.Len(t, edges, 2, "an ambiguous reference emits one edge per candidate")
		wantKey := groupKey("Foo", string(kgtypes.EdgeCalls), "app/main.go:Run", 0)
		for _, e := range edges {
			assert.InDelta(t, 0.5, e.Confidence, 1e-9, "Confidence is 1/N and NEITHER member is preferred")
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method, "the group is CLOSED")
			assert.Equal(t, wantKey, e.Evidence, "every member of one group shares one key")
			assert.NotEmpty(t, e.Evidence)
		}
		// SEQUENCE, NOT PREFERENCE: the own scope is gathered first so a
		// group's member edges are byte-identical across collects.
		assert.Equal(t, "app/own.go:Foo", edges[0].ToId)
		assert.Equal(t, "lib/lib.go:Foo", edges[1].ToId)

		// KNOWN-POSITIVE CONTROL.
		ctrl := resolveRef(ix, ref, "Local")
		assert.Equal(t, RefBound, ctrl.Status)
		assert.Equal(t, RuleOwnScope, ctrl.Rule)
	})

	t.Run("two_dots_multi_hit", func(t *testing.T) {
		// THIS PROGRAM DOES NOT COMPILE EITHER: two dot imports both exporting
		// Foo is "Foo redeclared in this block", again at declaration time and
		// regardless of whether Foo is used. Same union-of-build-configurations
		// reasoning as the row above.
		//
		// THE TWO SCOPE IDS ARE CHOSEN SO ASCENDING ORDER IS NOT THE ORDER THEY
		// WERE ADDED: the declarations go into the index zlib-first, and the
		// assertion below requires the edges to come out alib-first. Since the
		// site carries a MAP, whose range order Go randomizes, only the sort
		// can make that ordering hold.
		ix := indexOf(t,
			&declRec{NodeID: "zlib/z.go:Foo", File: "zlib/z.go", Scope: "dir:zlib", Name: "Foo"},
			&declRec{NodeID: "alib/a.go:Foo", File: "alib/a.go", Scope: "dir:alib", Name: "Foo"},
			// Declared in exactly ONE dot scope: the single cross-scope bind
			// that drives DotScopeBinds non-zero in the same run.
			&declRec{NodeID: "alib/a.go:Bar", File: "alib/a.go", Scope: "dir:alib", Name: "Bar"},
			&declRec{NodeID: "app/own.go:Local", File: "app/own.go", Scope: "dir:app", Name: "Local"},
		)
		ref := dotSiteFor("app/main.go", "dir:app", "dir:zlib", "dir:alib")

		got := resolveRef(ix, ref, "Foo")
		assert.Equal(t, RefAmbiguous, got.Status)
		assert.Equal(t, RuleDotScope, got.Rule)
		require.Len(t, got.Candidates, 2)

		results := []*treesitter.Result{{
			FilePath: "app/main.go", Language: treesitter.LangGo,
			Edges: []treesitter.Edge{
				refEdge("app/main.go:Run", "Foo", ref),
				refEdge("app/main.go:Run", "Bar", ref),
				refEdge("app/main.go:Run", "Local", ref),
			},
		}}
		nodeIDs := map[string]bool{
			"app/main.go:Run": true, "zlib/z.go:Foo": true, "alib/a.go:Foo": true,
			"alib/a.go:Bar": true, "app/own.go:Local": true,
		}
		edges, stats := resolveEdgesWithStats(results, ix, nodeIDs)

		// Two group members for Foo, one bound edge for Bar, one for Local.
		require.Len(t, edges, 4)
		wantKey := groupKey("Foo", string(kgtypes.EdgeCalls), "app/main.go:Run", 0)
		for _, e := range edges[:2] {
			assert.InDelta(t, 0.5, e.Confidence, 1e-9)
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
			assert.Equal(t, wantKey, e.Evidence)
		}
		// ASCENDING SCOPE ID — alib before zlib, the reverse of index order.
		assert.Equal(t, "alib/a.go:Foo", edges[0].ToId)
		assert.Equal(t, "zlib/z.go:Foo", edges[1].ToId)

		// THE COUNTERS, DRIVEN NON-ZERO. resolveEdgesWithStats is the only
		// function that returns them and no other prescribed test calls it, so
		// without this row a hardcoded zero would satisfy every string-grep
		// gate — and on a corpus measuring zero dot imports a hardcoded zero is
		// indistinguishable from a wired one for the life of the release.
		assert.Equal(t, 1, stats.DotScopeGroups, "one group, counted once and not once per member")
		assert.Equal(t, 1, stats.DotScopeBinds, "the single cross-scope bind in the same fixture")

		// KNOWN-POSITIVE CONTROL: Local binds in the OWN scope through the same
		// gathered rung, so a zero above is not an index that holds nothing.
		ctrl := resolveRef(ix, ref, "Local")
		assert.Equal(t, RefBound, ctrl.Status)
		assert.Equal(t, RuleOwnScope, ctrl.Rule)
	})

	t.Run("parented_reference_sees_dot_scopes", func(t *testing.T) {
		// THE FILL-IN-PLACE CATCHER, AND THE ONE ROW THAT GOES THROUGH THE REAL
		// PATH. A hand-built RefSite never passes through refForParent, which is
		// where the by-value copy happens — so against a hand-built site a
		// fillBinds that ASSIGNED a fresh DotScopes map would pass every row
		// above and this catcher would catch nothing. The arm is registered
		// BEFORE chunking because the chunker is what allocates the map.
		treesitter.RegisterBindsResolver(treesitter.LangGo,
			func(_ *treesitter.RepoContext, _ map[string]*treesitter.Result, self *treesitter.Result) treesitter.BindsResult {
				if self.FilePath == "app/main.go" {
					return treesitter.BindsResult{
						Binds:     map[string]treesitter.Bind{},
						DotScopes: []string{"dir:lib"},
					}
				}
				return treesitter.BindsResult{}
			})
		// RESTORE, NEVER DELETE — Go ships a real arm registered at init, and
		// unregistering it here would disarm it for every later test.
		t.Cleanup(func() { treesitter.RegisterGoBindsResolver() })

		results := chunkFixture(t, []fixtureFile{
			{path: "lib/lib.go", src: "" +
				"package lib\n\nfunc Helper() int {\n\treturn 1\n}\n"},
			{path: "app/main.go", src: "" +
				"package main\n\nimport . \"example.com/lib\"\n\n" +
				"func local() int {\n\treturn 2\n}\n\n" +
				"type Runner struct{}\n\n" +
				"func (r Runner) Run() int {\n\treturn Helper() + local()\n}\n"},
		})
		fillBinds(&treesitter.RepoContext{}, results)
		ix := indexResults(t, results)

		e := refEdgeFrom(t, results, "Helper")
		require.NotNil(t, e.Ref)
		require.NotEmpty(t, e.Ref.Parent,
			"the fixture's reference must come from a PARENTED declaration")
		require.True(t, e.Ref.DotScopes["dir:lib"],
			"the arm's dot scope must reach the PARENTED site — fill-in-place, never assign")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleDotScope, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "lib/lib.go:Helper", got.Candidates[0].NodeID)

		// KNOWN-POSITIVE CONTROL, on the same parented site: a name this file
		// declares itself still binds under the own-scope rule.
		ctrl := refEdgeFrom(t, results, "local")
		ctrlRes := resolveRef(ix, ctrl.Ref, ctrl.ToID)
		assert.Equal(t, RefBound, ctrlRes.Status)
		assert.Equal(t, RuleOwnScope, ctrlRes.Rule)
	})
}
