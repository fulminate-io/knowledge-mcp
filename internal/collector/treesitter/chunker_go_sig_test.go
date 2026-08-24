// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// goSigFixture declares, in one file, every pair the signature renderer must
// keep APART plus the one pair it must render ALIKE.
//
// THE KNOWN-NEGATIVE SET IS THE POINT. A subtest asserting only that matching
// signatures render equal is satisfied by a renderer that returns the same
// string for everything, so each positive below is paired with a near-miss that
// must NOT render equal to it.
const goSigFixture = `package app

type Foo struct{}
type K struct{}
type V struct{}
type C struct{}

type Impl struct{}

func (i Impl) Composed(k string) ([]Foo, error)  { return nil, nil }
func (i Impl) Bare(k string) (Foo, error)        { return Foo{}, nil }
func (i Impl) Variadic(v ...Foo) error           { return nil }
func (i Impl) Sliced(v []Foo) error              { return nil }
func (i Impl) TwoNames(a, b int) error           { return nil }
func (i Impl) OneName(a int) error               { return nil }
func (i Impl) FuncParam(f func(ctx C) error)     {}
func (i Impl) NamedParam(f C)                    {}
func (i Impl) Mapped(m map[K]V) error            { return nil }
func (i Impl) Valued(m V) error                  { return nil }

type Contract interface {
	Composed(k string) ([]Foo, error)
	Variadic(v ...Foo) error
	TwoNames(a, b int) error
	FuncParam(f func(ctx C) error)
	Mapped(m map[K]V) error
}
`

// sigOf indexes the fixture's chunks by "Parent.Name" and returns each
// declaration's rendered signature.
func sigOf(t *testing.T) map[string]*SigFacts {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "app/sig.go", []byte(goSigFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")

	out := map[string]*SigFacts{}
	for _, ch := range res.Chunks {
		if ch.TypeFacts == nil || ch.TypeFacts.Sig == nil {
			continue
		}
		out[ch.ParentName+"."+ch.Name] = ch.TypeFacts.Sig
	}
	// CONTROL: the map must be non-empty, or every comparison below compares two
	// nils and agrees perfectly.
	require.NotEmpty(t, out, "control: at least one declaration rendered a signature")
	return out
}

// sigKey flattens a SigFacts to one comparable string in the shape the derivation
// compares: "(params)(results)" with each expression's shape and leaves inlined.
func sigKey(s *SigFacts) string {
	if s == nil {
		return "<nil>"
	}
	render := func(es []TypeExpr) string {
		var out strings.Builder
		for i, e := range es {
			if i > 0 {
				out.WriteString(",")
			}
			out.WriteString(e.Shape)
			for _, l := range e.Leaves {
				out.WriteString("|" + l)
			}
		}
		return out.String()
	}
	return "(" + render(s.Params) + ")(" + render(s.Results) + ")"
}

// embedsFixture declares every embed shape the extractors must record and every
// shape they must decline, in one file.
const embedsFixture = `package app

import "example.com/ext"

type Base struct{}

type TokenSource interface{ Token() string }

type PlainStruct struct {
	Base
	C int
}

type StructEmbedsIface struct {
	TokenSource
	N int
}

type PlainIface interface {
	TokenSource
	M() error
}

type QualIface interface {
	ext.Reader
	M2() error
}

type Num interface {
	~int | ~float64
	comparable
}

type HasAnonIfaceField struct {
	X interface{ TokenSource }
	C int
}

type IfaceWithAnonStructParam interface {
	F(x struct{ Base }) error
}
`

// groupedFixture is separate because a grouped declaration must be the ONLY
// declaration under test — the decline is about the shared declaration node.
const groupedFixture = `package app

type Base struct{}

type (
	GroupedA struct {
		Base
	}
	GroupedB interface {
		Base
	}
)
`

// embedsOf chunks a fixture and returns each type declaration's recorded embeds,
// keyed by declaration name.
func embedsOf(t *testing.T, path, src string) map[string][]string {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")

	out := map[string][]string{}
	for _, ch := range res.Chunks {
		if ch.ChunkType != "type_declaration" {
			continue
		}
		if ch.TypeFacts == nil {
			out[ch.Name] = nil
			continue
		}
		out[ch.Name] = ch.TypeFacts.Embeds
	}
	require.NotEmpty(t, out, "control: at least one type declaration was seen")
	return out
}

// TestGoTypeFactsEmbeds pins what the two anchored embed extractors record and,
// just as load-bearing, what they refuse to record.
func TestGoTypeFactsEmbeds(t *testing.T) {
	embeds := embedsOf(t, "app/embeds.go", embedsFixture)

	t.Run("struct_embeds_recorded", func(t *testing.T) {
		require.Equal(t, []string{"Base"}, embeds["PlainStruct"],
			"an embedded struct field is recorded and the named field C is not")
		// A struct embedding an INTERFACE is the shape a promoted method set
		// depends on — a fake embedding a big interface satisfies it.
		require.Equal(t, []string{"TokenSource"}, embeds["StructEmbedsIface"])
	})

	t.Run("interface_embeds_recorded", func(t *testing.T) {
		require.Equal(t, []string{"TokenSource"}, embeds["PlainIface"],
			"an embedded in-repo interface is recorded and the method spec M is not an embed")
		require.Equal(t, []string{"ext.Reader"}, embeds["QualIface"],
			"a qualified embed keeps its package, so the parser can bind it through the file's imports")
	})

	t.Run("named_field_not_an_embed", func(t *testing.T) {
		require.NotContains(t, embeds["PlainStruct"], "C")
		require.NotContains(t, embeds["StructEmbedsIface"], "N")
		// KNOWN-POSITIVE CONTROL: these declarations DID record something, so the
		// absence above is a real exclusion and not an empty extractor.
		require.NotEmpty(t, embeds["PlainStruct"])
		require.NotEmpty(t, embeds["StructEmbedsIface"])
	})

	t.Run("type_set_union_declines", func(t *testing.T) {
		// `~int | ~float64` is ONE type_elem with TWO negated_type children and
		// declines by the exactly-one-named-child rule; `comparable` is a
		// single-child element and IS a spelling, declining later at resolution
		// because no in-repo scope declares it.
		require.Equal(t, []string{"comparable"}, embeds["Num"],
			"the union declines; the bare constraint name is recorded as a spelling")
	})

	t.Run("anon_iface_field_declines", func(t *testing.T) {
		require.Empty(t, embeds["HasAnonIfaceField"],
			"X is a NAMED field whose type is an anonymous interface — the declaration embeds nothing. "+
				"An unanchored descent for interface_type would report TokenSource here.")
	})

	t.Run("anon_struct_param_declines", func(t *testing.T) {
		require.Empty(t, embeds["IfaceWithAnonStructParam"],
			"an anonymous struct in a parameter is not this interface's embed. "+
				"An unanchored descent for struct_type would report Base here.")
	})

	t.Run("grouped_decl_declines", func(t *testing.T) {
		grouped := embedsOf(t, "app/grouped.go", groupedFixture)
		// CONTROL: the grouped declaration was actually seen, so the emptiness
		// below is a decline rather than a fixture that produced no chunk.
		require.NotEmpty(t, grouped, "control: the grouped fixture produced type declarations")
		for name, got := range grouped {
			if name == "Base" {
				continue
			}
			require.Empty(t, got,
				"%s is one of several specs sharing one declaration node, so neither spec's embeds "+
					"can be attributed; nil is the honest answer", name)
		}
	})
}

// embedEdgeFixture is the emission fixture. It is separate from embedsFixture
// because the EMISSION assertions are about EDGES, and an edge's target is the
// spelling AS WRITTEN — resolution against the repo's declarations happens
// later, in the parser, so a spelling naming nothing in-repo still produces an
// edge here and is dropped there.
const embedEdgeFixture = `package app

type Base struct{}

type TokenSource interface{ Token() string }

type PlainStruct struct {
	Base
	C int
}

type StructEmbedsIface struct {
	TokenSource
	N int
}

type PlainIface interface {
	TokenSource
	M() error
}

type UnionOnly interface {
	~int | ~float64
}

type IfaceWithAnonStructParam interface {
	F(x struct{ Base }) error
}
`

// embedEdgesFrom returns the EMBEDS edge targets emitted for one declaration.
func embedEdgesFrom(edges []Edge, from string) []string {
	var out []string
	for _, e := range edges {
		if e.Type == EdgeEmbeds && e.FromID == from {
			out = append(out, e.ToID)
		}
	}
	sort.Strings(out)
	return out
}

// TestGoInterfaceEmbedsEmitEdges pins that a Go interface's embedded elements
// reach the graph as EMBEDS edges, and that routing the emission arm through the
// combined extractor leaves every struct path exactly as it was.
func TestGoInterfaceEmbedsEmitEdges(t *testing.T) {
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "app/edges.go", []byte(embedEdgeFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")
	edges := res.Edges

	t.Run("iface_embed_emits", func(t *testing.T) {
		require.Equal(t, []string{"TokenSource"}, embedEdgesFrom(edges, "app.PlainIface"),
			"an interface embedding an in-repo interface emits one EMBEDS edge to it, "+
				"and its method spec M is not an embed")
	})

	t.Run("struct_embed_unchanged", func(t *testing.T) {
		// KNOWN-POSITIVE CONTROL. Without it, a change that broke the struct path
		// while adding the interface path would still show the new subtest green.
		require.Equal(t, []string{"Base"}, embedEdgesFrom(edges, "app.PlainStruct"),
			"a struct embedding a struct still emits exactly as before")
	})

	t.Run("struct_iface_unchanged", func(t *testing.T) {
		require.Equal(t, []string{"TokenSource"}, embedEdgesFrom(edges, "app.StructEmbedsIface"),
			"a struct embedding an interface still emits exactly ONE edge — routing through the "+
				"combined extractor must not double it by running both halves over one body")
	})

	t.Run("union_emits_none", func(t *testing.T) {
		require.Empty(t, embedEdgesFrom(edges, "app.UnionOnly"),
			"a type set is ONE element with several children and is not an embed")
	})

	t.Run("anon_param_emits_none", func(t *testing.T) {
		// CHARACTERIZATION GUARD, not a red-first. The false edge this asserts
		// against was removed when both extractors were anchored to the type_spec's
		// `type` field, one step before the emission arm was repointed — so this is
		// green before and after this step's own change. It guards that fix against
		// being undone by the repoint. Its red-first evidence lives in
		// TestGoTypeFactsEmbeds/anon_struct_param_declines.
		require.Empty(t, embedEdgesFrom(edges, "app.IfaceWithAnonStructParam"),
			"an anonymous struct inside a method signature is not the interface's embed")
	})
}

// TestGoSigTypeExpr pins that composition is visible in the rendered signature
// and that an interface method spec renders identically to the concrete method
// that satisfies it.
func TestGoSigTypeExpr(t *testing.T) {
	sigs := sigOf(t)

	get := func(t *testing.T, key string) *SigFacts {
		t.Helper()
		s, ok := sigs[key]
		require.True(t, ok, "fixture control: %s rendered a signature", key)
		return s
	}

	t.Run("composed_differs_from_bare", func(t *testing.T) {
		composed := sigKey(get(t, "Impl.Composed"))
		bare := sigKey(get(t, "Impl.Bare"))
		require.NotEqual(t, composed, bare,
			"([]Foo, error) must not render equal to (Foo, error) — the slice is part of the type")
		require.Contains(t, composed, "[]", "the slice composition survives into the shape")

		mapped := sigKey(get(t, "Impl.Mapped"))
		valued := sigKey(get(t, "Impl.Valued"))
		require.NotEqual(t, mapped, valued, "map[K]V must not render equal to V")
		require.Contains(t, mapped, "map[", "the map composition survives into the shape")

		fnParam := sigKey(get(t, "Impl.FuncParam"))
		namedParam := sigKey(get(t, "Impl.NamedParam"))
		require.NotEqual(t, fnParam, namedParam,
			"a func-typed parameter must not render equal to a bare named type")
		require.Contains(t, fnParam, "func(", "the nested signature survives into the shape")
	})

	t.Run("variadic_differs_from_slice", func(t *testing.T) {
		variadic := sigKey(get(t, "Impl.Variadic"))
		sliced := sigKey(get(t, "Impl.Sliced"))
		require.NotEqual(t, variadic, sliced,
			"...Foo and []Foo are different types and must render differently")
		require.Contains(t, variadic, "...", "the variadic marker survives into the shape")
	})

	t.Run("names_expand_per_name", func(t *testing.T) {
		two := get(t, "Impl.TwoNames")
		one := get(t, "Impl.OneName")
		require.Len(t, two.Params, 2,
			"`a, b int` is TWO int parameters sharing one type node, not one")
		require.Len(t, one.Params, 1)
		require.NotEqual(t, sigKey(two), sigKey(one),
			"a two-parameter signature must not render equal to a one-parameter one")
	})

	t.Run("spec_and_method_render_alike", func(t *testing.T) {
		// THE MATCHING PREMISE. A spec's parameter_list and its satisfying
		// method's are structurally identical in this grammar, and the receiver
		// is deliberately absent from both renderings — so equality here is the
		// property the whole derivation rests on.
		for _, m := range []string{"Composed", "Variadic", "TwoNames", "FuncParam", "Mapped"} {
			require.Equal(t, sigKey(get(t, "Impl."+m)), sigKey(get(t, "Contract."+m)),
				"%s: the interface spec and the concrete method must render the same signature", m)
		}
		// KNOWN-NEGATIVE CONTROL for this subtest: equality above must not be
		// coming from a renderer that returns one string for everything.
		require.NotEqual(t, sigKey(get(t, "Contract.Composed")), sigKey(get(t, "Contract.Variadic")),
			"control: two different specs still render differently")
	})
}
