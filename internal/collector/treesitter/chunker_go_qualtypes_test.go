// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkQualFixture chunks one in-memory fixture through the real Chunker, at a
// path whose extension selects the language under test.
func chunkQualFixture(t *testing.T, path, src string) *Result {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks")
	return res
}

// qualTypesFor returns the qualifier-type map carried by the reference site of
// the declaration whose qualified edge source ends in fromSuffix.
//
// It reads the map off an EDGE rather than off a chunk because the reference
// site is what the resolution ladder actually consults: a map that never
// reached an edge's Ref would be invisible to the rung no matter how correctly
// the walk built it. Every fixture below therefore makes a call, so its
// declaration emits at least one reference edge to carry the site.
func qualTypesFor(t *testing.T, res *Result, fromSuffix string) map[string]QualType {
	t.Helper()
	for i := range res.Edges {
		e := &res.Edges[i]
		if e.Ref != nil && strings.HasSuffix(e.FromID, fromSuffix) {
			return e.Ref.QualifierTypes
		}
	}
	t.Fatalf("no reference-carrying edge found from a declaration ending in %q", fromSuffix)
	return nil
}

// TestQualifierTypesPerDeclaration pins the field's PER-DECLARATION nature,
// which is what separates it from every other field on RefSite.
//
// Binds and DotScopes are per-FILE maps filled in place through the shared
// site; QualifierTypes is built per declaration and ASSIGNED, so a declaration
// carrying one must not be handed the shared file-level pointer. If
// refForDeclaration ever regressed to refForParent, both declarations below
// would read one map and each would see the other's locals — the exact leak
// this test exists to catch.
func TestQualifierTypesPerDeclaration(t *testing.T) {
	const src = `package p

func first() {
	a := Alpha{}
	a.Do()
}

func second() {
	b := Beta{}
	b.Do()
}
`
	res := chunkQualFixture(t, "pkg/two_decls.go", src)

	firstTypes := qualTypesFor(t, res, "first")
	secondTypes := qualTypesFor(t, res, "second")

	// KNOWN-POSITIVE CONTROL. Without it, a walk that bound nothing at all
	// would make both maps nil, and "the two maps differ" plus "neither sees
	// the other's name" would both hold vacuously.
	require.NotEmpty(t, firstTypes, "control: the first declaration bound at least one qualifier")
	require.NotEmpty(t, secondTypes, "control: the second declaration bound at least one qualifier")

	assert.Equal(t, QualType{Text: "Alpha"}, firstTypes["a"])
	assert.Equal(t, QualType{Text: "Beta"}, secondTypes["b"])

	// The separation itself: neither declaration can see the other's local.
	_, firstSeesB := firstTypes["b"]
	_, secondSeesA := secondTypes["a"]
	assert.False(t, firstSeesB, "first must not see second's local — the sites are not shared")
	assert.False(t, secondSeesA, "second must not see first's local — the sites are not shared")
}

// TestQualifierTypesGoOnly covers the Go arm's binding rules, the conflict
// rule, and the nil-map contract an UNARMED language keeps.
//
// THE NAME NO LONGER DESCRIBES THE REGISTRY AND IS RETAINED DELIBERATELY.
// Several landed criteria grep this test by its exact name, and a criterion's
// command lives in the knowledge graph rather than in source — so a repo-wide
// rename structurally cannot find them, and renaming here would leave those
// gates permanently red against correct work. TestQualifierArmRegistrationAllowlist
// is the current authority on WHICH languages carry an arm; this test is the
// authority on what the GO arm does and on what an unarmed language still gets.
func TestQualifierTypesGoOnly(t *testing.T) {
	t.Run("binds_receiver_params_and_locals", func(t *testing.T) {
		const src = `package p

func (s *Server) Handle(p Req, r, t Extra) (out Result) {
	var v Explicit
	var untyped = 3
	c := &Thing{}
	d := Other{}
	e := Make()
	g, h := Pair()
	fn := func(z Zed) { z.Use() }
	_ = fn
	s.Log()
	return out
}
`
		res := chunkQualFixture(t, "pkg/sig.go", src)
		got := qualTypesFor(t, res, "Server.Handle")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers")

		assert.Equal(t, QualType{Text: "Server"}, got["s"], "receiver, pointer stripped")
		assert.Equal(t, QualType{Text: "Req"}, got["p"], "parameter")
		assert.Equal(t, QualType{Text: "Extra"}, got["r"], "two names share one type")
		assert.Equal(t, QualType{Text: "Extra"}, got["t"], "two names share one type")
		assert.Equal(t, QualType{Text: "Result"}, got["out"], "named result is a qualifier too")
		assert.Equal(t, QualType{Text: "Explicit"}, got["v"], "var with an explicit type")
		assert.Equal(t, QualType{Text: "Thing"}, got["c"], "address of a composite literal")
		assert.Equal(t, QualType{Text: "Other"}, got["d"], "composite literal")
		assert.Equal(t, QualType{Text: "Make", FromCall: true}, got["e"], "single-result call")
		assert.Equal(t, QualType{Text: "Zed"}, got["z"], "closure parameter, at depth")

		// The multi-value form indexes into the callee's result list.
		assert.Equal(t, QualType{Text: "Pair", FromCall: true, ResultIndex: 0}, got["g"])
		assert.Equal(t, QualType{Text: "Pair", FromCall: true, ResultIndex: 1}, got["h"])

		// A var with no explicit type infers from its value, which this arm
		// does not do — so it binds nothing rather than guessing.
		_, ok := got["untyped"]
		assert.False(t, ok, "a var with no declared type binds nothing")
	})

	t.Run("binds_type_assertion", func(t *testing.T) {
		const src = `package p

func assertShape(iface any) {
	v := iface.(Concrete)
	v.Do()
}
`
		res := chunkQualFixture(t, "pkg/assert.go", src)
		got := qualTypesFor(t, res, "assertShape")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers")
		assert.Equal(t, QualType{Text: "Concrete"}, got["v"],
			"the asserted type is the qualifier's type, and it is not FromCall")
	})

	t.Run("conflicting_rebind_is_dropped", func(t *testing.T) {
		// A shadowed name bound to two different types. The simulation this
		// rung reproduces declines such a qualifier rather than picking one,
		// because picking manufactures wrong targets in exactly the population
		// the zero-wrong-targets gate covers.
		const src = `package p

func shadow(cond bool) {
	x := Alpha{}
	if cond {
		x := Beta{}
		x.Do()
	}
	x.Do()
	keep := Gamma{}
	keep.Do()
}
`
		res := chunkQualFixture(t, "pkg/shadow.go", src)
		got := qualTypesFor(t, res, "shadow")

		// KNOWN-POSITIVE CONTROL: a sibling name in the SAME declaration still
		// binds. Without it, an arm that bound nothing would pass the
		// absence assertion below while proving nothing about conflicts.
		assert.Equal(t, QualType{Text: "Gamma"}, got["keep"],
			"control: an unconflicted name in the same declaration still binds")

		_, ok := got["x"]
		assert.False(t, ok, "a name bound to two different types is dropped, not resolved to one")
	})

	t.Run("declines_containers_and_conversions", func(t *testing.T) {
		const src = `package p

func decline() {
	var s []*pkg.T
	var m map[string]V
	ch := make(chan int)
	w := (*Wrapped)(nil)
	keep := Kept{}
	keep.Do()
	_ = s
	_ = m
	_ = ch
	_ = w
}
`
		res := chunkQualFixture(t, "pkg/decline.go", src)
		got := qualTypesFor(t, res, "decline")

		// KNOWN-POSITIVE CONTROL for a test whose other assertions are all
		// absences: a bindable name in the same declaration must still bind.
		assert.Equal(t, QualType{Text: "Kept"}, got["keep"],
			"control: a bindable name in the same declaration still binds")

		for _, name := range []string{"s", "m"} {
			_, ok := got[name]
			assert.False(t, ok, "a container-typed value has no methods, so %q declines", name)
		}
		// A conversion is not a call to an in-repo declaration: its callee is a
		// parenthesized_expression, which carries no name to look up.
		if qt, ok := got["w"]; ok {
			assert.NotEqual(t, "Wrapped", qt.Text, "a conversion must not bind its target type")
		}
	})

	t.Run("unarmed_languages_map_nil", func(t *testing.T) {
		// The nil map is the whole of the unregistered contract: it is what
		// sends refForDeclaration down the refForParent branch and keeps every
		// other language byte-identical rather than merely equivalent.
		//
		// THE SUBJECT LIST COMES FROM THE CAPABILITY CENSUS, not from a literal
		// "every language but Go", and the skip below is what makes it durable:
		// a language armed by later work drops out of the subject set instead of
		// redding against that work. Three successive waves have armed languages
		// this list once named, and each time the surviving assertion was the
		// census-driven one.
		//
		// THE FIXTURE LANGUAGES ARE THEREFORE CHOSEN FROM THE DELIBERATELY
		// UNARMED END of the census rather than from the merely not-yet-armed
		// end: lua and bash declare no type annotation and no conformance at
		// all, so their rows state a property of the language that no later
		// group can flip.
		//
		// ELIXIR IS THE SHARPEST FIXTURE HERE and is why the set is not lua
		// alone. It is an ARMED language — its type-facts arm captures
		// @behaviour conformance — that still carries no QUALIFIER arm, because
		// the language has no receiver-dispatch call form to bind. Its nil map
		// therefore shows the qualifier registry is genuinely per-language,
		// rather than showing only that nothing has touched the language yet.
		//
		// EVERY FIXTURE BELOW WAS MEASURED TO PRODUCE AT LEAST ONE REFERENCE
		// SITE, which the loop's own control requires: an elm and an ocaml
		// fixture were tried first and produce none, so their nil-map claim
		// would have been unobservable rather than true.
		fixtures := map[Language]struct{ path, src string }{
			LangLua:    {"pkg/sample.lua", "local M = {}\n\nfunction M.m()\n  helper()\nend\n\nreturn M\n"},
			LangBash:   {"pkg/sample.sh", "#!/usr/bin/env bash\n\nm() {\n  helper foo\n}\n"},
			LangElixir: {"lib/sample.ex", "defmodule Sample do\n  def m(x) do\n    Helper.go(x)\n  end\nend\n"},
		}
		checked := 0
		for lang, fx := range fixtures {
			if testTypedQualifierCensus[lang].QualifierArm {
				// This language has since been armed; its own row-flipping
				// change owns the assertion, and asserting nil here would red
				// against that correct work.
				continue
			}
			checked++
			path, src := fx.path, fx.src
			res := chunkQualFixture(t, path, src)

			var sawRef bool
			for i := range res.Edges {
				e := &res.Edges[i]
				if e.Ref == nil {
					continue
				}
				sawRef = true
				assert.Nil(t, e.Ref.QualifierTypes,
					"%s: no arm is registered for this language, so the map stays nil", path)
			}
			// KNOWN-POSITIVE CONTROL. A language whose fixture produced no
			// reference sites at all would satisfy the loop above vacuously.
			assert.True(t, sawRef, "%s control: the fixture produced at least one reference site", path)
		}
		// KNOWN-POSITIVE CONTROL on the census-driven subject list itself: if
		// every fixture language had been armed, the loop above would have
		// iterated nothing and this subtest would prove nothing at all.
		require.Positive(t, checked,
			"every fixture language is now armed, so this subtest asserted nothing: it needs an unarmed fixture language")

		// THE OTHER HALF OF THE CONTROL, and the one that matters most: the
		// nil above must mean "no arm for this language", not "the field is
		// never populated for anyone". Go, in the same binary, is non-nil.
		goRes := chunkQualFixture(t, "pkg/control.go", "package p\n\nfunc f() {\n\tc := Ctl{}\n\tc.Do()\n}\n")
		assert.NotNil(t, qualTypesFor(t, goRes, "f"),
			"control: the registered Go arm DOES populate the field, so nil is language-specific")
	})
}
