// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// emitFixture is the emission fixture: a three-level chain so a promoted method
// has a DECLARING type distinct from the type that gains it.
const emitFixture = `package svc

type Grand interface {
	Deep() int
}

type Iface interface {
	Grand
	Mid() int
}

type impl struct{}

func (i impl) Deep() int { return 1 }
func (i impl) Mid() int  { return 2 }

type wrapper struct {
	Iface
}
`

// implementsEdgesOf populates a fixture and returns only its IMPLEMENTS edges.
func implementsEdgesOf(t *testing.T, files []fixtureFile) []*knowledgev1.Edge {
	t.Helper()
	res := populateFixture(t, files)
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if e.Type == string(kgtypes.EdgeImplements) {
			out = append(out, e)
		}
	}
	require.NotEmpty(t, out, "control: the fixture emitted IMPLEMENTS edges at all")
	return out
}

// implEdgeBetween returns the IMPLEMENTS edge from→to, or nil when absent.
func implEdgeBetween(edges []*knowledgev1.Edge, from, to string) *knowledgev1.Edge {
	for _, e := range edges {
		if e.FromId == from && e.ToId == to {
			return e
		}
	}
	return nil
}

// TestImplementsEdgeEmission pins the shape of the emitted edges: both levels,
// the cardinality carrier, and the resolution path they must NOT take.
func TestImplementsEdgeEmission(t *testing.T) {
	edges := implementsEdgesOf(t, []fixtureFile{{path: "svc/emit.go", src: emitFixture}})

	t.Run("type_level", func(t *testing.T) {
		e := implEdgeBetween(edges, "svc/emit.go:Iface", "svc/emit.go:impl")
		require.NotNil(t, e,
			"the interface's TYPE declaration points at the concrete type declaration")
		assert.Equal(t, string(kgtypes.EdgeImplements), e.Type)
	})

	t.Run("method_level", func(t *testing.T) {
		require.NotNil(t, implEdgeBetween(edges, "svc/emit.go:Iface.Mid", "svc/emit.go:impl.Mid"),
			"the interface METHOD SPEC points at the method satisfying it — the hop a caller "+
				"standing on a call's target takes to reach implementers")
		// DIRECTION IS FROM THE INTERFACE OUTWARD; the reverse edge must not exist.
		assert.Nil(t, implEdgeBetween(edges, "svc/emit.go:impl.Mid", "svc/emit.go:Iface.Mid"),
			"the edge is emitted in ONE direction only")
	})

	t.Run("method_set_on_method", func(t *testing.T) {
		e := implEdgeBetween(edges, "svc/emit.go:Iface.Mid", "svc/emit.go:impl.Mid")
		require.NotNil(t, e)
		assert.Equal(t, kgtypes.EdgeMethodMethodSet+"2", e.Method,
			"Iface's expanded set is Mid plus Deep promoted from Grand, so the cardinality is 2")
		// KNOWN-POSITIVE CONTROL for the number itself: the one-method interface
		// beside it carries a DIFFERENT value, so this is not a constant.
		g := implEdgeBetween(edges, "svc/emit.go:Grand.Deep", "svc/emit.go:impl.Deep")
		require.NotNil(t, g)
		assert.Equal(t, kgtypes.EdgeMethodMethodSet+"1", g.Method)
	})

	t.Run("weight_zero", func(t *testing.T) {
		// EVERY IMPLEMENTS EDGE CARRIES WEIGHT 0, like CONTAINS / IMPORTS /
		// USES_TYPE / EMBEDS. The cardinality rides Method precisely so it never
		// reaches the weighted topology analyzers, which normalize a zero weight
		// to the 1.0 baseline and would otherwise invert the intent.
		for _, e := range edges {
			assert.InDelta(t, 0.0, e.Weight, 0,
				"IMPLEMENTS is an unweighted edge type: %s -> %s", e.FromId, e.ToId)
		}
	})

	t.Run("promoted_declarer", func(t *testing.T) {
		// wrapper gains Deep and Mid by embedding Iface, and has no method
		// declaration of its own. Its METHOD-level edges therefore target the
		// DECLARING type's method nodes — no synthetic node is invented for a
		// copy that has no source location.
		require.NotNil(t, implEdgeBetween(edges, "svc/emit.go:Iface", "svc/emit.go:wrapper"),
			"the type-level edge still names the promoting type")
		assert.Nil(t, implEdgeBetween(edges, "svc/emit.go:Iface.Mid", "svc/emit.go:wrapper.Mid"),
			"no node exists for wrapper's promoted copy, so no edge names one")
		require.NotNil(t, implEdgeBetween(edges, "svc/emit.go:Iface.Mid", "svc/emit.go:impl.Mid"),
			"control: the declaring type's method node IS the target")
	})

	t.Run("no_self_satisfying_method_edge", func(t *testing.T) {
		// wrapper EMBEDS Iface, so its promoted method set holds Iface's OWN spec
		// records and the method-level projection would emit <spec> -> <spec>.
		// A declaration does not implement itself, so those pairs are suppressed
		// at the emitter rather than collapsed downstream.
		for _, e := range edges {
			assert.NotEqual(t, e.FromId, e.ToId,
				"a method spec cannot implement ITSELF: %s -> %s", e.FromId, e.ToId)
		}
		// THE CONTROL IS LOAD-BEARING: without it an emitter that derived NOTHING
		// would satisfy the loop above vacuously.
		require.NotNil(t, implEdgeBetween(edges, "svc/emit.go:Iface", "svc/emit.go:wrapper"),
			"control: embedding an interface still satisfies it at the TYPE level")
	})

	t.Run("bypasses_resolution", func(t *testing.T) {
		// An IMPLEMENTS edge names node IDs on BOTH ends already, so it never
		// enters reference resolution — and the observable proof is that it
		// carries none of the residue fields a resolved reference edge gets.
		for _, e := range edges {
			assert.Empty(t, e.Evidence,
				"a group key is resolution residue; an IMPLEMENTS edge has none: %s -> %s",
				e.FromId, e.ToId)
			assert.InDelta(t, 0.0, e.Confidence, 0,
				"a 1/N confidence is resolution residue too: %s -> %s", e.FromId, e.ToId)
			assert.NotEqual(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
			assert.NotEqual(t, kgtypes.EdgeMethodDynamic, e.Method)
		}
	})
}

// derivedPairs indexes a derivation by "<ifaceNodeID> => <concreteNodeID>".
func derivedPairs(pairs []implementsPair) map[string]implementsPair {
	out := map[string]implementsPair{}
	for _, p := range pairs {
		out[p.iface.NodeID+" => "+p.concrete.NodeID] = p
	}
	return out
}

// deriveOver builds an index from fixture files and runs the derivation.
func deriveOver(t *testing.T, files []fixtureFile) (map[string]implementsPair, implementsStats) {
	t.Helper()
	ix := indexResults(t, chunkFixture(t, files))
	pairs, stats := deriveImplements(ix)
	return derivedPairs(pairs), stats
}

// TestDeriveImplements covers the three measured precision rules and the four
// dispositions the derivation must make explicit.
//
// EVERY NEGATIVE SUBTEST CARRIES A KNOWN-POSITIVE CONTROL IN THE SAME FIXTURE.
// A derivation that produces nothing at all satisfies every "require NO pair"
// assertion, so each such subtest also requires a pair it SHOULD find.
func TestDeriveImplements(t *testing.T) {
	t.Run("resolved_identity", func(t *testing.T) {
		// RULE (a). Two packages declare an interface with the SAME method shape
		// and a result type spelled identically — but each spelling names its OWN
		// package's type, so the two signatures are different types and neither
		// package's implementer satisfies the other's interface.
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "alpha/a.go", src: `package alpha

type Res struct{}

type Sink interface {
	Emit() Res
}

type impl struct{}

func (i impl) Emit() Res { return Res{} }
`},
			{path: "beta/b.go", src: `package beta

type Res struct{}

type Sink interface {
	Emit() Res
}

type impl struct{}

func (i impl) Emit() Res { return Res{} }
`},
		})

		// KNOWN-POSITIVE CONTROL: each package's OWN implementer IS derived.
		// Without it this subtest passes on an empty derivation.
		require.Contains(t, pairs, "alpha/a.go:Sink => alpha/a.go:impl",
			"control: the same-package implementer is derived")
		require.Contains(t, pairs, "beta/b.go:Sink => beta/b.go:impl",
			"control: and so is the other package's")

		assert.NotContains(t, pairs, "alpha/a.go:Sink => beta/b.go:impl",
			"beta's Res is not alpha's Res, so beta's impl does not satisfy alpha's Sink")
		assert.NotContains(t, pairs, "beta/b.go:Sink => alpha/a.go:impl")
	})

	t.Run("unexported_confines", func(t *testing.T) {
		// RULE (b). An interface with an unexported method can only be satisfied
		// from the package that declares that method name.
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "alpha/a.go", src: `package alpha

type Hidden interface {
	visible() int
}

type near struct{}

func (n near) visible() int { return 1 }
`},
			{path: "beta/b.go", src: `package beta

type far struct{}

func (f far) visible() int { return 1 }
`},
		})

		require.Contains(t, pairs, "alpha/a.go:Hidden => alpha/a.go:near",
			"control: the SAME-package implementer is derived")
		assert.NotContains(t, pairs, "alpha/a.go:Hidden => beta/b.go:far",
			"an unexported method name is qualified by its declaring package, so no other "+
				"package can satisfy the interface")
	})

	t.Run("embeds_recurse", func(t *testing.T) {
		// RULE (c), interface chain: A embeds B embeds C. A type declaring the
		// whole chain's methods satisfies A. Positive by construction — the defect
		// this rule fixes is a FALSE NEGATIVE.
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "svc/chain.go", src: `package svc

type C interface {
	Cee() int
}

type B interface {
	C
	Bee() int
}

type A interface {
	B
	Ay() int
}

type all struct{}

func (a all) Ay() int  { return 1 }
func (a all) Bee() int { return 2 }
func (a all) Cee() int { return 3 }

type partial struct{}

func (p partial) Ay() int { return 1 }
`},
		})

		require.Contains(t, pairs, "svc/chain.go:A => svc/chain.go:all",
			"A's method set is Ay+Bee+Cee once both embed hops are walked")
		assert.Equal(t, 3, pairs["svc/chain.go:A => svc/chain.go:all"].methodSetSize)
		// KNOWN-NEGATIVE: a type declaring only the outermost method does NOT
		// satisfy A, so the recursion is not simply admitting everything.
		assert.NotContains(t, pairs, "svc/chain.go:A => svc/chain.go:partial",
			"a type declaring only Ay does not satisfy A")
	})

	t.Run("struct_embeds_interface", func(t *testing.T) {
		// THE THREE-LEVEL SHAPE, and the fixture is three levels deep on purpose.
		// A one-hop implementation gains Iface's DIRECT specs and stops, missing
		// Grand's entirely — so it would pass an Iface assertion and fail a Grand
		// one. A two-level fixture cannot tell those apart.
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "svc/deep.go", src: `package svc

type Grand interface {
	Deep() int
}

type Iface interface {
	Grand
	Mid() int
}

type Both interface {
	Deep() int
	Mid() int
}

type fake struct {
	Iface
}
`},
		})

		require.Contains(t, pairs, "svc/deep.go:Iface => svc/deep.go:fake",
			"LEVEL 1: a struct embedding an interface promotes that interface's method set")
		require.Contains(t, pairs, "svc/deep.go:Grand => svc/deep.go:fake",
			"LEVEL 2+3: and the methods of the interfaces IT embeds — the hop a one-level "+
				"implementation silently drops")
		require.Contains(t, pairs, "svc/deep.go:Both => svc/deep.go:fake",
			"an interface whose set is covered only once the grandchild's methods are promoted in")
	})

	t.Run("method_set_size", func(t *testing.T) {
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "svc/size.go", src: `package svc

type Inner interface {
	One() int
}

type Outer interface {
	Inner
	Two() int
}

type impl struct{}

func (i impl) One() int { return 1 }
func (i impl) Two() int { return 2 }
`},
		})
		p, ok := pairs["svc/size.go:Outer => svc/size.go:impl"]
		require.True(t, ok, "control: the pair is derived at all")
		assert.Equal(t, 2, p.methodSetSize,
			"the size counts the PROMOTED set — Two declared here plus One promoted from Inner")
		assert.Len(t, p.methods, 2, "and one attribution per method in that set")
		// The one-method interface beside it keeps its own size, so the assertion
		// above is not reading a constant.
		inner, ok := pairs["svc/size.go:Inner => svc/size.go:impl"]
		require.True(t, ok)
		assert.Equal(t, 1, inner.methodSetSize)
	})

	t.Run("empty_derives_nothing", func(t *testing.T) {
		pairs, _ := deriveOver(t, []fixtureFile{
			{path: "svc/empty.go", src: `package svc

type Any interface{}

type Real interface {
	Do() int
}

type impl struct{}

func (i impl) Do() int { return 1 }
`},
		})
		// KNOWN-POSITIVE CONTROL, and this subtest is worthless without it: a
		// derivation returning nothing for everything satisfies the assertion below.
		require.Contains(t, pairs, "svc/empty.go:Real => svc/empty.go:impl",
			"control: the NON-empty interface beside it IS derived")
		assert.NotContains(t, pairs, "svc/empty.go:Any => svc/empty.go:impl",
			"every type satisfies an empty interface, so a pair would carry no information")
	})

	t.Run("generic_undecided", func(t *testing.T) {
		pairs, stats := deriveOver(t, []fixtureFile{
			{path: "svc/gen.go", src: `package svc

type Box[T any] interface {
	Get() T
}

type Plain interface {
	Get() int
}

type impl struct{}

func (i impl) Get() int { return 1 }
`},
		})
		assert.Positive(t, stats.GenericUndecided,
			"an interface whose method set names a TYPE PARAMETER is counted as undecided, so a "+
				"reader can tell 'could not decide' from 'no implementers'")
		assert.NotContains(t, pairs, "svc/gen.go:Box => svc/gen.go:impl",
			"and it derives nothing rather than guessing")
		// KNOWN-POSITIVE CONTROL: the non-generic interface beside it IS decided,
		// so the counter is not simply skipping everything.
		require.Contains(t, pairs, "svc/gen.go:Plain => svc/gen.go:impl",
			"control: the non-generic interface is still derived")
		assert.Positive(t, stats.Interfaces, "control: at least one interface was decided")
	})

	t.Run("extembed_still_derives", func(t *testing.T) {
		// THE DISPOSITION, PINNED. ExtEmbed marks an under-known method set; it
		// does NOT gate the derivation. A later reader turning the flag into a gate
		// fails here, and declRec.ExtEmbed's doc comment names this subtest.
		pairs, stats := deriveOver(t, []fixtureFile{
			{path: "svc/ext.go", src: `package svc

type Closer interface {
	error
	Close() int
}

type impl struct{}

func (i impl) Close() int { return 1 }
`},
		})
		require.Contains(t, pairs, "svc/ext.go:Closer => svc/ext.go:impl",
			"an interface embedding an out-of-repo name is STILL matched against a type covering "+
				"its KNOWN specs — skipping it would turn a possible false positive into a certain "+
				"false negative on an extremely common shape")
		assert.Positive(t, stats.ExtEmbedPairs,
			"and the exposure is REPORTED, so the false-positive risk is visible rather than folded "+
				"into the totals")
	})
}
