// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// fieldHopSrc declares a type whose FIELD carries the method, reached as
// `o.In.Method()` — the one-struct-field hop.
const fieldHopSrc = `package svc

type Inner struct{}

func (i *Inner) Method() {}

type Outer struct {
	In Inner
}

func run(o Outer) {
	o.In.Method()
}
`

// TestR2TStructFieldHop asserts BOTH directions of the hop's rule.
//
// A subtest asserting only the positive half is satisfied by a rule that
// over-matches in exactly the direction the negative half exists to catch: the
// segment count must be EXACTLY two, and a deeper chain must be left at today's
// behavior rather than followed.
func TestR2TStructFieldHop(t *testing.T) {
	t.Run("binds_through_one_field", func(t *testing.T) {
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: fieldHopSrc}}, "svc/svc.go", "o.In.Method")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Outer"}, e.Ref.QualifierTypes["o"],
			"control: segment 0 is typed, which is the hop's precondition")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule,
			"the field hop is a different ROUTE to the same fact, so it reports the same rule")
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "svc/svc.go:Inner.Method", got.Candidates[0].NodeID)
	})

	t.Run("ambiguous_owner_declines", func(t *testing.T) {
		// THE EXACTLY-ONE-OWNER GUARD. Two files declare the SAME type name
		// with DIFFERENT field types — the build-tag-split shape — so which
		// field table to read is genuinely unknown. Reading the head of the set
		// is a wrong-target generator whose wrong answer is a real declaration
		// with a plausible name, which is exactly why no fixture surfaces it by
		// accident. Same construction as ambiguous_callee_declines one function
		// over; this is that guard's twin, and it was specified, implemented,
		// commented as load-bearing, and ungated until now.
		const a = `package svc

type Beta struct{}

func (b *Beta) Only() {}

type Holder struct {
	F Beta
}
`
		const b = `package svc

type Gamma struct{}

func (g *Gamma) Only() {}

type Holder struct {
	F Gamma
}
`
		const caller = `package svc

func run(h Holder) {
	h.F.Only()
}
`
		ix, e := resolveGoFixtureRef(t, []fixtureFile{
			{path: "svc/a.go", src: a},
			{path: "svc/b.go", src: b},
			{path: "svc/caller.go", src: caller},
		}, "svc/caller.go", "h.F.Only")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Holder"}, e.Ref.QualifierTypes["h"],
			"control: segment 0 is typed, so the hop reaches the owner lookup")
		// CONTROL: the owner key really is ambiguous. Without this the subtest
		// could pass because Holder resolved to nothing at all.
		require.Len(t, ix.lookup(declKey{Scope: "dir:svc", Name: "Holder"}), 2,
			"control: two declarations share the owning type's key")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"an ambiguous owner declines; reading either field table would be a coin-flip bind")
	})

	t.Run("declines_two_dot_qualifier", func(t *testing.T) {
		// THE g_chain STRATUM, left at today's behavior: 91 groups on
		// knowledge and 85 on agent. The rule is a SEGMENT COUNT, not a split
		// at the first separator — splitting `a.b.c` at the first dot yields
		// two pieces and would wrongly proceed.
		const src = `package svc

type Deepest struct{}

func (d *Deepest) Method() {}

type Middle struct {
	Deep Deepest
}

type Top struct {
	Mid Middle
}

func run(t Top) {
	t.Mid.Deep.Method()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "t.Mid.Deep.Method")

		require.NotNil(t, e.Ref)
		// CONTROL: segment 0 IS typed and the chain is genuinely resolvable
		// field-by-field, so the decline is about the SEGMENT COUNT rather
		// than about the fixture failing to set the hop up.
		require.Equal(t, treesitter.QualType{Text: "Top"}, e.Ref.QualifierTypes["t"],
			"control: a one-hop rule would have had everything it needed except the depth")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"a three-segment qualifier is left at today's behavior")
	})
}

// TestR2TFieldHopConsumerAxis gates FieldTypes' FIRST PRODUCTION READER on the
// axis that reader indexes.
//
// FieldTypes is a MAP KEYED BY FIELD NAME, so the axis is the KEY. This is the
// fourth consumer in this ticket to index a producer's output, and the three
// before it all shipped green with the axis untested — the text axis, the
// position axis of ResultTypes, and the call-return arm's guards, each found by
// defect injection rather than by reading. This test exists so the fourth is
// caught by a gate instead of by a review round.
func TestR2TFieldHopConsumerAxis(t *testing.T) {
	// holderSrc declares TWO fields of DIFFERENT types with the method on the
	// SECOND one only, so binding is possible ONLY by selecting the correct
	// key. A reader that takes the first field, or any field, reddens.
	const holderSrc = `package svc

type Alpha struct{}

type Beta struct{}

func (b *Beta) Only() {}

type Holder struct {
	Fa Alpha
	Fb Beta
}

func run(h Holder) {
	h.Fb.Only()
}
`

	t.Run("selects_by_field_name", func(t *testing.T) {
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: holderSrc}}, "svc/svc.go", "h.Fb.Only")

		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Holder"}, e.Ref.QualifierTypes["h"])

		// CONTROL ON THE OTHER KEY: both fields must be recorded with
		// DIFFERENT types, or the fixture cannot discriminate a key-selecting
		// reader from a first-field one.
		owners := ix.lookup(declKey{Scope: "dir:svc", Name: "Holder"})
		require.Len(t, owners, 1)
		require.Equal(t, typeRef{Scope: "dir:svc", Name: "Alpha"}, owners[0].FieldTypes["Fa"])
		require.Equal(t, typeRef{Scope: "dir:svc", Name: "Beta"}, owners[0].FieldTypes["Fb"])

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "svc/svc.go:Beta.Only", got.Candidates[0].NodeID,
			"the method belongs to the SECOND field's type; reaching it proves the key was selected")
	})

	t.Run("missing_field_declines", func(t *testing.T) {
		// CHARACTERIZATION GUARD, green before AND after the reader landed —
		// labeled as such rather than claimed red-first. Its sibling
		// selects_by_field_name is the red-first half; this one pins that an
		// absent key declines rather than panicking or falling back to another
		// field, and it DOES redden under the any-field mutation, which is what
		// makes the pair discriminating rather than merely present.
		const src = `package svc

type Beta struct{}

func (b *Beta) Only() {}

type Holder struct {
	Fb Beta
}

func run(h Holder) {
	h.Absent.Only()
}
`
		ix, e := resolveGoFixtureRef(t,
			[]fixtureFile{{path: "svc/svc.go", src: src}}, "svc/svc.go", "h.Absent.Only")

		require.NotNil(t, e.Ref)
		// CONTROL: the owning type IS resolvable and DOES carry a field table,
		// so the decline is about the missing KEY rather than about the hop
		// never reaching the table at all.
		owners := ix.lookup(declKey{Scope: "dir:svc", Name: "Holder"})
		require.Len(t, owners, 1)
		require.NotNil(t, owners[0].FieldTypes, "control: the field table exists")
		_, present := owners[0].FieldTypes["Absent"]
		require.False(t, present, "control: the key really is absent")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.NotEqual(t, RuleTypedQualifier, got.Rule,
			"an absent field key DECLINES — never a panic, never a wrong bind")
	})
}
