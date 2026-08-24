// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// r2tServerSrc declares a type with a method and calls that method through a
// LOCAL VARIABLE — the shape the whole rung exists for. Without R2T the
// qualifier `s` is neither a bind nor a declared parent, so the reference falls
// to R3 and returns every same-named declaration in the directory.
const r2tServerSrc = `package svc

type Server struct{}

func (s *Server) Handle() {}

func run() {
	s := Server{}
	s.Handle()
}
`

// TestR2TBindOnlyFallThrough is the rung's contract test: it binds when it can,
// and when it cannot the ladder continues EXACTLY as it does today.
func TestR2TBindOnlyFallThrough(t *testing.T) {
	t.Run("binds_when_type_has_method", func(t *testing.T) {
		// RED-FIRST ANCHOR. With the rung absent this returns RuleDynamicScope,
		// because `s` is a local variable that no import bound and no
		// declaration parents. It is also the KNOWN-POSITIVE CONTROL for the
		// byte-identity subtest below: without it, that guard would pass
		// vacuously in a world where the rung never fires at all.
		ix, e := resolveFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: r2tServerSrc}},
			"svc/svc.go", treesitter.EdgeCalls, "s.Handle")

		require.NotNil(t, e.Ref)
		require.NotNil(t, e.Ref.QualifierTypes,
			"control: the chunker recorded qualifier types for this declaration")
		require.Equal(t, treesitter.QualType{Text: "Server"}, e.Ref.QualifierTypes["s"],
			"control: `s` is recorded as a Server, which is what the rung resolves through")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule,
			"the qualifier is a VALUE whose declared type carries the method")
		assert.Equal(t, "typed-qualifier", string(got.Rule),
			"the rule's recorded VALUE is what a resolution audit reads")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "svc/svc.go:Server.Handle", got.Candidates[0].NodeID)
	})

	t.Run("byte_identical_when_type_lacks_method", func(t *testing.T) {
		// CHARACTERIZATION GUARD, green before AND after — labeled as such
		// rather than claimed red-first. The qualifier IS typed, but the type
		// has no such method, so the rung must decline and the ladder must
		// produce precisely what it produced before the rung existed.
		//
		// The comparison is over the FULL emitted edge slice — candidates,
		// ORDER, Method and Evidence group key — because all four are part of
		// edge identity and of the per-row contribution hash. A rung that
		// reordered a group while keeping its members would churn every
		// affected row's hash on the next collect.
		const src = `package svc

type Holder struct{}

func (h *Holder) Only() {}

func other() {}

func run() {
	h := Holder{}
	h.Missing()
}
`
		files := []fixtureFile{{path: "svc/svc.go", src: src}}

		armed := populateFixture(t, files)

		treesitter.UnregisterQualifierTypes(treesitter.LangGo)
		treesitter.UnregisterTypeFacts(treesitter.LangGo)
		// RESTORE, never merely delete: an arm left unregistered would disarm
		// the rung for every later test in this binary.
		t.Cleanup(func() {
			treesitter.RegisterGoQualifierTypes()
			treesitter.RegisterGoTypeFacts()
		})
		unarmed := populateFixture(t, files)

		require.Len(t, armed.Edges, len(unarmed.Edges),
			"the rung declined, so it added and removed no edge")
		for i := range armed.Edges {
			a, u := armed.Edges[i], unarmed.Edges[i]
			assert.Equalf(t, u.FromId, a.FromId, "edge %d FromId", i)
			assert.Equalf(t, u.ToId, a.ToId, "edge %d ToId", i)
			assert.Equalf(t, u.Type, a.Type, "edge %d Type", i)
			assert.Equalf(t, u.Method, a.Method, "edge %d Method", i)
			assert.Equalf(t, u.Evidence, a.Evidence, "edge %d Evidence group key", i)
			// DELTA ZERO IS DELIBERATE: this is a byte-identity guard, so the
			// two confidences must be the SAME value, not merely close. The
			// delta form is used only because Confidence is a float and the
			// linter refuses a bare equality on one.
			assert.InDeltaf(t, u.Confidence, a.Confidence, 0, "edge %d Confidence", i)
		}
	})

	t.Run("interface_typed_qualifier_falls_through", func(t *testing.T) {
		// NO INTERFACE DETECTION IS WRITTEN ANYWHERE, and this subtest is what
		// proves the outcome follows from the INDEX rather than from a branch
		// that knows what an interface is.
		//
		// THE INTERFACE IS DELIBERATELY OUT-OF-SCOPE. An IN-REPO interface's
		// method specs are chunked and indexed under Parent=<Interface>, so the
		// step-4 lookup HITS and the rung binds — the two-hop targeting the
		// contract-node model exists for, asserted on its own in
		// resolve_walk_typed_iface_test.go. An interface declared outside the
		// indexed scope has no spec to index, so the same lookup misses and the
		// ladder continues to R3 exactly as it always did. Same code, opposite
		// outcomes, decided entirely by whether a declaration is present.
		const src = `package svc

import "example.com/ext"

type Impl struct{}

func (i *Impl) Do() {}

func run(d ext.Doer) {
	d.Do()
}
`
		ix, e := resolveFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}},
			"svc/svc.go", treesitter.EdgeCalls, "d.Do")

		require.NotNil(t, e.Ref)
		// CONTROL: the qualifier IS typed, and typed to the interface. Without
		// this the subtest could pass because the chunker recorded nothing at
		// all, which would prove something entirely different.
		require.Equal(t, treesitter.QualType{Text: "ext.Doer"}, e.Ref.QualifierTypes["d"],
			"control: `d` is recorded as an ext.Doer, so the rung really is being asked about an interface")
		require.True(t, ix.hasScope("dir:svc"),
			"control: the declaring scope is indexed, so an empty lookup is about the PARENT key")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"an interface with no indexed method specs binds NOTHING here and falls through to R3")
	})
}

// TestR2TNonGoByteIdentical pins requirement 5: the rung is registered for Go
// only, so every other language resolves exactly as it did before.
//
// It asserts on a language whose qualified references exercise the SAME ladder
// rungs the Go arm sits between, so a rung that leaked across languages would
// change a rule here rather than merely failing to fire.
func TestR2TNonGoByteIdentical(t *testing.T) {
	files := []fixtureFile{
		{path: "app/animals.py", src: "class Animal:\n    def speak(self):\n        return 1\n\n\ndef run():\n    a = Animal()\n    a.speak()\n"},
		{path: "app/Main.java", src: "class Main {\n    void go() {}\n\n    void run() {\n        Main m = new Main();\n        m.go();\n    }\n}\n"},
		{path: "app/lib.ts", src: "export class Thing {\n  act() {}\n}\n\nexport function run() {\n  const t = new Thing();\n  t.act();\n}\n"},
	}

	armed := populateFixture(t, files)

	treesitter.UnregisterQualifierTypes(treesitter.LangGo)
	treesitter.UnregisterTypeFacts(treesitter.LangGo)
	t.Cleanup(func() {
		treesitter.RegisterGoQualifierTypes()
		treesitter.RegisterGoTypeFacts()
	})
	unarmed := populateFixture(t, files)

	// KNOWN-POSITIVE CONTROL: the fixture must actually produce edges, or the
	// equality below compares two empty slices and proves nothing.
	require.NotEmpty(t, armed.Edges, "control: the non-Go fixture produced edges")
	require.Len(t, armed.Edges, len(unarmed.Edges))

	for i := range armed.Edges {
		a, u := armed.Edges[i], unarmed.Edges[i]
		assert.Equalf(t, u.FromId, a.FromId, "edge %d FromId", i)
		assert.Equalf(t, u.ToId, a.ToId, "edge %d ToId", i)
		assert.Equalf(t, u.Type, a.Type, "edge %d Type", i)
		assert.Equalf(t, u.Method, a.Method, "edge %d Method", i)
		assert.Equalf(t, u.Evidence, a.Evidence, "edge %d Evidence group key", i)
		// Delta zero: exact identity, per the note on the sibling guard above.
		assert.InDeltaf(t, u.Confidence, a.Confidence, 0, "edge %d Confidence", i)
	}
}
