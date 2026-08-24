// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeclRecSigAndEmbeds covers the four claims the index-time resolution makes
// about the new declaration-record fields.
//
// THE FIRST SUBTEST IS THE MEASURED PRECISION RULE: a signature key is resolved,
// not textual, so the SAME SPELLING in two packages yields two different keys. A
// fixture that derived both sides from one declaration could not show this, so
// the two packages declare their own SubCollectorResult and their own Collect
// returning it, in different directories.
func TestDeclRecSigAndEmbeds(t *testing.T) {
	const alphaSrc = `package alpha

type Req struct{}

type SubCollectorResult struct{}

type Collector interface {
	Collect(r Req) (SubCollectorResult, error)
}
`
	const betaSrc = `package beta

type Req struct{}

type SubCollectorResult struct{}

type Collector interface {
	Collect(r Req) (SubCollectorResult, error)
}
`
	const embedSrc = `package emb

import ext "example.com/ext"

type Base struct{}

type LocalEmbedder struct {
	Base
	N int
}

type ExternalEmbedder struct {
	ext.Base
	N int
}

type Plain struct {
	N int
}

type Contract interface {
	Do() error
}
`
	// A CONCRETE IMPLEMENTER IN A DIFFERENT PACKAGE from the interface it
	// satisfies. This is the shape that catches a leaf renderer which
	// scope-qualifies predeclared types: alpha's spec would render
	// `dir:alpha` + error and this method `dir:gamma` + error, and the two would
	// never match despite being the same signature.
	const gammaSrc = `package gamma

import "example.com/alpha"

type Impl struct{}

type Box struct{}

func (i Impl) Do() error { return nil }

func (i Impl) Take(s string, n int) (bool, error) { return false, nil }

func (i Impl) Local(b Box) error { return nil }

func (i Impl) Cross(r alpha.Req) (alpha.SubCollectorResult, error) {
	return alpha.SubCollectorResult{}, nil
}
`

	ix := indexResults(t, chunkFixture(t, []fixtureFile{
		{path: "alpha/a.go", src: alphaSrc},
		{path: "beta/b.go", src: betaSrc},
		{path: "emb/e.go", src: embedSrc},
		{path: "gamma/g.go", src: gammaSrc},
	}))

	t.Run("same_spelling_diff_pkgs_differ", func(t *testing.T) {
		a := recFor(t, ix, "alpha/a.go:Collector.Collect")
		b := recFor(t, ix, "beta/b.go:Collector.Collect")
		// CONTROL: both keys are non-empty, or the inequality below would hold
		// between two empty strings for the wrong reason.
		require.NotEmpty(t, a.SigKey, "control: the alpha spec resolved a signature key")
		require.NotEmpty(t, b.SigKey, "control: the beta spec resolved a signature key")
		assert.NotEqual(t, a.SigKey, b.SigKey,
			"the same SPELLING in two packages is two different TYPES, so the keys must differ")
		assert.Contains(t, a.SigKey, "dir:alpha", "the leaf carries the scope it resolved into")
		assert.Contains(t, b.SigKey, "dir:beta")
	})

	t.Run("unbound_leaf_renders_ext", func(t *testing.T) {
		a := recFor(t, ix, "alpha/a.go:Collector.Collect")
		assert.Contains(t, a.SigKey, "ext:error",
			"a leaf that names no in-repo scope renders ext:<name> — an EMPTY rendering would make "+
				"two different signatures compare equal, which is the false-match direction")
		assert.NotContains(t, a.SigKey, "()()",
			"control: the key is not the degenerate empty signature")
		// The separators are consumed, never left in the output.
		assert.NotContains(t, a.SigKey, "\x1f", "every leaf placeholder was substituted")
	})

	t.Run("dropped_embed_sets_extembed", func(t *testing.T) {
		local := recFor(t, ix, "emb/e.go:LocalEmbedder")
		external := recFor(t, ix, "emb/e.go:ExternalEmbedder")
		plain := recFor(t, ix, "emb/e.go:Plain")

		// KNOWN-POSITIVE CONTROL: an embed that DOES resolve is recorded and sets
		// no flag, so the flag below is a real signal and not a constant true.
		require.Len(t, local.Embeds, 1, "a locally-declared embed resolves and is kept")
		assert.Equal(t, typeRef{Scope: "dir:emb", Name: "Base"}, local.Embeds[0])
		assert.False(t, local.ExtEmbed)

		assert.Empty(t, external.Embeds, "an embed naming no in-repo scope is DROPPED")
		assert.True(t, external.ExtEmbed,
			"and the drop is recorded, because this declaration's promoted method set is now a LOWER BOUND")

		assert.Empty(t, plain.Embeds, "a declaration with no embedded field records none")
		assert.False(t, plain.ExtEmbed, "and nothing was dropped, so the flag stays clear")
	})

	t.Run("interface_flag_set", func(t *testing.T) {
		contract := recFor(t, ix, "emb/e.go:Contract")
		plain := recFor(t, ix, "emb/e.go:Plain")
		assert.True(t, contract.IsInterface, "an interface declaration is marked as one")
		assert.False(t, plain.IsInterface, "control: a struct declaration is not")
	})

	t.Run("predeclared_types_match_across_packages", func(t *testing.T) {
		// THE REGRESSION THIS SUBTEST EXISTS FOR. `error` and every other
		// predeclared type carries no package qualifier, and the shared type-text
		// resolver maps an unqualified name to the DECLARING file's own scope. If
		// a predeclared leaf were rendered that way, an interface's spec and an
		// implementer in another package would disagree on every signature
		// mentioning error, string or int — which is nearly all of them — and the
		// disagreement would be invisible to any same-package fixture.
		spec := recFor(t, ix, "emb/e.go:Contract.Do")
		impl := recFor(t, ix, "gamma/g.go:Impl.Do")
		require.NotEmpty(t, spec.SigKey, "control: the spec resolved a key")
		assert.Equal(t, spec.SigKey, impl.SigKey,
			"a spec and a cross-package implementer of the same signature must render the SAME key")
		assert.Equal(t, "()(ext:error)", impl.SigKey,
			"a predeclared type renders ext:<name>, carrying no package scope at all")

		take := recFor(t, ix, "gamma/g.go:Impl.Take")
		assert.Equal(t, "(ext:string,ext:int)(ext:bool,ext:error)", take.SigKey,
			"every predeclared leaf renders ext:, in source order, one entry per name")

		// KNOWN-POSITIVE CONTROL for the OTHER direction: a NON-predeclared type
		// still carries its resolved scope, so the rule above did not simply turn
		// every leaf into ext:.
		local := recFor(t, ix, "gamma/g.go:Impl.Local")
		assert.Equal(t, "(dir:gamma"+sigKeyLeafSep+"Box)(ext:error)", local.SigKey,
			"an in-repo type still renders scope-qualified beside a predeclared one")

		// And a QUALIFIED type whose import this harness leaves unbound declines
		// to ext: rather than inventing a scope — the same decline the shared
		// resolver documents, reached through the signature renderer.
		cross := recFor(t, ix, "gamma/g.go:Impl.Cross")
		assert.Equal(t, "(ext:alpha.Req)(ext:alpha.SubCollectorResult,ext:error)", cross.SigKey,
			"an unbound qualifier declines to ext:<qualifier>.<name>, never to the empty string — and it "+
				"KEEPS the qualifier, so two external packages declaring one type name stay distinct")
	})

	t.Run("receiver_absent_from_the_key", func(t *testing.T) {
		// The property the whole derivation rests on, asserted where the key is
		// actually built: a spec's key names only its parameters and results, so
		// a concrete method's receiver cannot make the two differ.
		a := recFor(t, ix, "alpha/a.go:Collector.Collect")
		assert.Equal(t, 1, strings.Count(a.SigKey, ")("),
			"the key is exactly (params)(results) — no third group for a receiver")
	})
}
