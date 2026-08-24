// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// resolveGoFixtureRef is resolveFixtureRef with the MODULE PATH supplied.
//
// The sibling harness hands fillBinds an EMPTY RepoContext, which is sufficient
// for every arm that reads its bind out of the import specifier's own text. The
// Go arm cannot work that way: it maps an import path onto a repo directory, so
// without ModulePath it binds nothing and a case about import-versus-local
// precedence would silently test neither.
func resolveGoFixtureRef(
	t *testing.T, files []fixtureFile, file, target string,
) (*declIndex, *treesitter.Edge) {
	t.Helper()
	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{ModulePath: "example.com/fixture"}, results)
	ix := indexResults(t, results)
	return ix, refEdgeIn(t, results, file, treesitter.EdgeCalls, target)
}

// TestR2TCallReturnArm covers the call-return arm of resolveTypedQualifier —
// the branch reached when a qualifier was bound from a CALL rather than from a
// type written directly.
//
// EVERY SUBTEST HERE EXISTS BECAUSE THE ARM'S GUARDS WERE UNGATED. The arm runs
// on the order of ten thousand times per corpus in production, yet a panic
// planted in its first statement was hit by no test in the module, and four
// separate mutations inside it all passed the full suite. A comment calling a
// guard load-bearing is not a gate; these are.
func TestR2TCallReturnArm(t *testing.T) {
	t.Run("single_result_callee_binds", func(t *testing.T) {
		const src = `package svc

type Conn struct{}

func (c *Conn) Send() {}

func Dial() Conn {
	return Conn{}
}

func run() {
	c := Dial()
	c.Send()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "c.Send")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Dial", FromCall: true},
			e.Ref.QualifierTypes["c"],
			"control: `c` is recorded as the RESULT of Dial, which is what routes this through the call arm")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "svc/svc.go:Conn.Send", got.Candidates[0].NodeID)
	})

	t.Run("result_index_selects_the_right_slot", func(t *testing.T) {
		// THE INDEX INJECTION. The callee returns TWO DIFFERENT types and the
		// method exists on the SECOND one only, so binding is possible only by
		// consuming ResultTypes[1]. An arm that reads ResultTypes[0] — the
		// mutation that passed the entire suite — reaches First, finds no
		// Only, and declines. This is the axis the consumer indexes, tested on
		// that axis rather than on the values it reads.
		const src = `package svc

type First struct{}

type Second struct{}

func (s *Second) Only() {}

func Two() (First, Second) {
	return First{}, Second{}
}

func run() {
	a, b := Two()
	b.Only()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "b.Only")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Two", FromCall: true, ResultIndex: 1},
			e.Ref.QualifierTypes["b"],
			"control: `b` is slot ONE of Two's result list")
		// CONTROL ON THE OTHER SLOT: `a` must be slot zero, or the fixture is
		// not actually exercising a two-slot list.
		require.Equal(t, treesitter.QualType{Text: "Two", FromCall: true, ResultIndex: 0},
			e.Ref.QualifierTypes["a"])

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "svc/svc.go:Second.Only", got.Candidates[0].NodeID,
			"slot 1 is Second, so the method must be Second's")
	})

	t.Run("short_result_list_declines", func(t *testing.T) {
		// THE BOUNDS CHECK, and it is not defensive: this shape occurs on the
		// real corpora, where deleting the check turns a resolution into a
		// COLLECTOR PANIC. The left side takes two names from a callee that
		// declares ONE result, so slot 1 does not exist.
		const src = `package svc

type First struct{}

func (f *First) Only() {}

func One() First {
	return First{}
}

func run() {
	a, b := One()
	b.Only()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "b.Only")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "One", FromCall: true, ResultIndex: 1},
			e.Ref.QualifierTypes["b"],
			"control: `b` asks for slot ONE, which One's single-result list does not have")

		// The assertion is that this RESOLVES AT ALL rather than panicking, and
		// that the rung declined rather than binding something.
		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"an out-of-range result slot DECLINES; it must never index past the recorded arity")
	})

	t.Run("container_result_declines", func(t *testing.T) {
		// THE PHASE 2 / PHASE 3 SEAM, end to end. The callee returns a
		// CONTAINER, whose text declined at chunk time to the empty string
		// while HOLDING its slot; this rung then consumes that slot and must
		// decline in turn. It gates the two halves agreeing — a producer that
		// dropped the slot instead of holding it, or a consumer that treated an
		// empty text as resolvable, both surface here.
		const src = `package svc

type First struct{}

func (f *First) Only() {}

func Many() []First {
	return nil
}

func run() {
	m := Many()
	m.Only()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "m.Only")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Many", FromCall: true},
			e.Ref.QualifierTypes["m"],
			"control: `m` is the result of Many, so the call arm really is entered")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"a slice result has no methods, so the slot declines and the rung falls through")
	})

	t.Run("ambiguous_callee_declines", func(t *testing.T) {
		// EXACTLY ONE, never the head of a set. Two declarations share the
		// callee's key, so which result list to read is genuinely unknown —
		// and picking the first is a wrong-target generator whose wrong answer
		// is a real declaration with a plausible name.
		const a = `package svc

type Conn struct{}

func (c *Conn) Send() {}

func Dial() Conn {
	return Conn{}
}
`
		const b = `package svc

func Dial() Conn {
	return Conn{}
}
`
		const caller = `package svc

func run() {
	c := Dial()
	c.Send()
}
`
		ix, e := resolveGoFixtureRef(t, []fixtureFile{
			{path: "svc/a.go", src: a},
			{path: "svc/b.go", src: b},
			{path: "svc/caller.go", src: caller},
		}, "svc/caller.go", "c.Send")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Dial", FromCall: true},
			e.Ref.QualifierTypes["c"])
		// CONTROL: the callee key really is ambiguous. Without this the subtest
		// could pass because Dial resolved to nothing at all.
		require.Len(t, ix.lookup(declKey{Scope: "dir:svc", Name: "Dial"}), 2,
			"control: two declarations share the callee key")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"an ambiguous callee declines; taking the head of the set would be a wrong target")
	})
}

// TestR2TDirectArmGuards covers the DIRECT-TYPE arm's in-repo lookup guard,
// which the cumulative review proved LOAD-BEARING after three earlier rounds
// had reasoned it was mere legibility.
func TestR2TDirectArmGuards(t *testing.T) {
	t.Run("orphan_receiver_declines", func(t *testing.T) {
		// THE DISCOVERY-EXCLUSION SHAPE, and it is ordinary rather than exotic:
		// a METHOD is indexed under Parent:Server while the TYPE Server itself
		// is NOT in the indexed corpus — which is what a build-tag split, a
		// partially-indexed repo, or a discovery exclusion produces. The
		// fixture omits the file declaring Server and supplies only the file
		// declaring its method.
		//
		// WITHOUT THE GUARD THIS BINDS: step 4 looks up
		// {Scope, Parent:Server, Name:Handle} and FINDS the orphan method, so
		// the rung would confidently answer a reference whose qualifier type
		// the corpus never declared. The guard's job is that conservatism.
		const methodOnly = `package svc

func (s *Server) Handle() {}
`
		const caller = `package svc

func run(s Server) {
	s.Handle()
}
`
		ix, e := resolveGoFixtureRef(t, []fixtureFile{
			{path: "svc/a.go", src: methodOnly},
			{path: "svc/caller.go", src: caller},
		}, "svc/caller.go", "s.Handle")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Server"}, e.Ref.QualifierTypes["s"],
			"control: the qualifier IS typed to Server, so the arm reaches the guard")
		// CONTROL A: the METHOD is indexed under Parent:Server — so step 4
		// genuinely would find it, which is what makes the guard load-bearing
		// rather than redundant.
		require.Len(t, ix.lookup(declKey{Scope: "dir:svc", Parent: "Server", Name: "Handle"}), 1,
			"control: the orphan method IS indexed, so step 4 would bind it")
		// CONTROL B: the TYPE is NOT indexed — the whole premise of the shape.
		require.Empty(t, ix.lookup(declKey{Scope: "dir:svc", Name: "Server"}),
			"control: the owning type is absent from the indexed corpus")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefDynamic, got.Status,
			"the rung declines and the reference falls through to R3")
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"a qualifier whose TYPE is not in the indexed corpus must not bind, even when its METHODS are")
	})
}

// TestR2TLadderGuards covers the two guards OUTSIDE the arm body: the ladder's
// !bound precedence, and the suffixed-name narrowing applied at step 4.
func TestR2TLadderGuards(t *testing.T) {
	t.Run("imported_package_beats_local_shadow", func(t *testing.T) {
		// THE !bound GUARD'S STATED FAILURE MODE, gated at last. A local
		// variable shadows an IMPORTED PACKAGE NAME. R1 has already answered
		// for a bound qualifier, and Go's own profile says imports beat locals,
		// so R2T must not re-derive a type for it and hand the reference to the
		// local. Removing the guard makes the local win.
		const other = `package other

type Thing struct{}

func (t *Thing) Do() {}
`
		const svc = `package svc

import "example.com/fixture/other"

type Local struct{}

func (l *Local) Do() {}

func run() {
	other := Local{}
	other.Do()
}
`
		ix, e := resolveGoFixtureRef(t, []fixtureFile{
			{path: "other/thing.go", src: other},
			{path: "svc/svc.go", src: svc},
		}, "svc/svc.go", "other.Do")

		require.NotNil(t, e.Ref)
		// CONTROL A: the qualifier IS bound by the import, which is the whole
		// precondition of the guard.
		require.NotEmpty(t, e.Ref.Binds["other"].Scope,
			"control: the import genuinely bound `other`, so the guard is under test")
		// CONTROL B: the qualifier is ALSO recorded as a local of type Local,
		// so the rung would have something to bind if the guard were absent.
		require.Equal(t, treesitter.QualType{Text: "Local"}, e.Ref.QualifierTypes["other"],
			"control: the local shadow exists and is typed, so a missing guard would bind it")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"a BOUND qualifier is R1's to answer; the local must not win")
		// AND IT MUST NOT BE CONFIDENTLY BOUND AT ALL. Removing the guard makes
		// this a RefBound single answer pointing at the local's method; with the
		// guard the reference falls to R3, which returns an OPEN set. The local
		// legitimately appears IN that open set — R3 returns every same-named
		// declaration in the directory — so the distinction this guard draws is
		// between a confident wrong answer and an admitted-ambiguous one, which
		// is what this asserts rather than the local's mere absence.
		assert.NotEqual(t, RefBound, got.Status,
			"the shadowed reference must not resolve to ONE confident answer")
	})

	t.Run("narrow_reduces_suffix_collided_set", func(t *testing.T) {
		// STEP 4 APPLIES narrow AS EVERY OTHER RUNG DOES. Two same-named
		// methods on one type collide, so the chunker suffixes their names with
		// "#"+astPathHash and the index holds two records under one key. A
		// reference carrying the SUFFIX names exactly one of them; without
		// narrow the rung re-presents a settled narrowing as an ambiguous group.
		//
		// THE TARGET IS BUILT FROM THE INDEX rather than hardcoded, because the
		// suffix is a content hash: hardcoding it would make this test a
		// tautology against whatever the hash happens to be today.
		const src = `package svc

type Server struct{}

func (s *Server) Handle() {}

func (s *Server) Handle() {}

func run() {
	s := Server{}
	s.Handle()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "s.Handle")

		require.NotNil(t, e.Ref)
		collided := ix.lookup(declKey{Scope: "dir:svc", Parent: "Server", Name: "Handle"})
		require.Len(t, collided, 2,
			"control: the two methods really did collide onto one index key")

		// Recover ONE member's suffixed name from its node ID and address it.
		dot := strings.LastIndex(collided[0].NodeID, ".")
		require.Positive(t, dot)
		suffixed := collided[0].NodeID[dot+1:]
		require.NotEqual(t, "Handle", suffixed,
			"control: the collided declaration really carries a disambiguating suffix")

		got := resolveRef(ix, e.Ref, "s."+suffixed)
		assert.Equal(t, RefBound, got.Status,
			"the suffix names ONE declaration, so the rung binds rather than grouping")
		assert.Equal(t, RuleTypedQualifier, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, collided[0].NodeID, got.Candidates[0].NodeID)
	})
}
