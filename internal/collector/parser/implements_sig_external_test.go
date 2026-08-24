// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// indexBoundResults is indexResults with the import-binds pass RUN, under a
// declared module path.
//
// IT IS A SEPARATE HELPER ON PURPOSE. indexResults leaves Binds nil, so every
// qualified spelling in every fixture using it declines to ext: for the trivial
// reason that no import ever bound — which is exactly the state in which the
// aliased-versus-unaliased divergence is INVISIBLE. A fixture that cannot make
// an import bind cannot observe a defect about how imports bind.
// It returns the chunks alongside the index for the same reason indexCorpus
// does: the leaf-space controls need the WRITTEN spellings, which live on the
// chunks and are published by neither the index nor a populate result.
//
// THE MODULE PATH IS FIXED HERE rather than passed in. Every fixture in this
// package declares its files under fixtureModulePath, and what each one varies
// is the IMPORT — aliased or not, under the module or outside it — which is
// what these tests are actually about. A parameter that every caller passes the
// same value to reads as a knob nobody turns.
const fixtureModulePath = "example.com/mod"

func indexBoundResults(t *testing.T, files []fixtureFile) (*declIndex, []corpusChunk) {
	t.Helper()
	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{ModulePath: fixtureModulePath}, results)
	DeduplicateChunks(results)

	total := 0
	for _, r := range results {
		total += len(r.Chunks)
	}
	ix := newDeclIndex(total)
	out := make([]corpusChunk, 0, total)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
			out = append(out, corpusChunk{chunk: chunk, ref: r.Ref})
		}
	}
	ix.resolveSigKeys()
	return ix, out
}

// TestSigKeyExternalLeafCollapsesBothImportSpellings is the reproduction for the
// aliased-versus-unaliased external leaf.
//
// THE TWO SPELLINGS ARE THE WHOLE FIXTURE. `checkout.Session` is written
// identically in both files; only the IMPORT differs. A non-aliased import binds
// under the path's LAST SEGMENT — `v83`, a version directory nobody writes in
// code — so the qualifier `checkout` finds no bind and the leaf declines. The
// aliased import binds `checkout` and the leaf resolves to a scope no file in
// the repository declares into. One type, two keys, and an interface and its
// implementer that disagree about nothing but import style.
func TestSigKeyExternalLeafCollapsesBothImportSpellings(t *testing.T) {
	const alphaSrc = `package alpha

import "github.com/vendor/checkout/v83"

import "example.com/mod/gamma"

type Contract interface {
	Pay(s checkout.Session) error
	Local(g gamma.Thing) error
}
`
	const betaSrc = `package beta

import checkout "github.com/vendor/checkout/v83"

import gamma "example.com/mod/gamma"

type Impl struct{}

func (Impl) Pay(s checkout.Session) error { return nil }

func (Impl) Local(g gamma.Thing) error { return nil }
`
	const gammaSrc = `package gamma

type Thing struct{}
`

	ix, _ := indexBoundResults(t, []fixtureFile{
		{path: "alpha/a.go", src: alphaSrc},
		{path: "beta/b.go", src: betaSrc},
		{path: "gamma/g.go", src: gammaSrc},
	})

	spec := recFor(t, ix, "alpha/a.go:Contract.Pay")
	impl := recFor(t, ix, "beta/b.go:Impl.Pay")
	require.NotEmpty(t, spec.SigKey, "control: the spec resolved a key")
	require.NotEmpty(t, impl.SigKey, "control: the implementer resolved a key")

	assert.Equal(t, "(ext:checkout.Session)(ext:error)", spec.SigKey,
		"a type outside the indexed universe renders ext:<qualifier>.<name> under the NON-ALIASED import")
	assert.Equal(t, "(ext:checkout.Session)(ext:error)", impl.SigKey,
		"and identically under the ALIASED one — the leaf is the type's identity, not the importer's style")
	assert.Equal(t, spec.SigKey, impl.SigKey,
		"both import spellings of one external type must collapse to ONE leaf — the qualifier is what "+
			"the SOURCE writes (`checkout.Session` in both files), never what the import happens to bind")

	// KNOWN-POSITIVE CONTROL, and the assertion that keeps the rule from being
	// "render everything ext:". An IN-REPO package reached through an equally
	// aliased import still renders scope-qualified, because the index holds
	// declarations in that scope.
	t.Run("in_repo_leaf_still_scope_qualified", func(t *testing.T) {
		specLocal := recFor(t, ix, "alpha/a.go:Contract.Local")
		implLocal := recFor(t, ix, "beta/b.go:Impl.Local")
		want := "(dir:gamma" + sigKeyLeafSep + "Thing)(ext:error)"
		assert.Equal(t, want, specLocal.SigKey,
			"an indexed scope is still carried on the leaf — the rule tests the INDEX, not the string")
		assert.Equal(t, want, implLocal.SigKey)
		require.True(t, ix.hasScope("dir:gamma"),
			"control: the fixture's in-repo scope genuinely contributes declarations")
		require.False(t, ix.hasScope("dir:github.com/vendor/checkout/v83"),
			"control: the fixture's external scope genuinely contributes none")
	})

	// AND THE DERIVATION ACTUALLY BINDS, which is the point of the whole key.
	// Without this the test would prove two strings equal while the edge the
	// equality exists to produce stayed absent.
	t.Run("derivation_binds_the_implementer", func(t *testing.T) {
		pairs, _ := deriveImplements(ix)
		found := false
		for _, p := range pairs {
			if p.iface.NodeID == "alpha/a.go:Contract" && p.concrete.NodeID == "beta/b.go:Impl" {
				found = true
			}
		}
		assert.True(t, found,
			"the cross-package implementer satisfies the interface once both leaves render alike")
	})
}

// TestSigKeyDeferredUntilIndexComplete pins the ordering the ext: rule depends
// on.
//
// THE DEFECT THIS PREVENTS IS ORDER-DEPENDENT AND SILENT. The leaf rule asks the
// index whether a scope contributes declarations; the index learns that as
// declarations are added. Were the key rendered during the build, a file indexed
// BEFORE the package its signature names would render ext:T while a file indexed
// after renders dir:pkg\x00T — the same never-matching divergence this work
// exists to remove, reintroduced through the fix for it and visible only under
// some file orderings.
//
// The fixture puts the CONSUMER first in index order and its declaring package
// last, which is precisely the order that breaks under an eager render.
func TestSigKeyDeferredUntilIndexComplete(t *testing.T) {
	const consumerSrc = `package consumer

import "example.com/mod/provider"

type Contract interface {
	Take(t provider.Payload) error
}
`
	const providerSrc = `package provider

type Payload struct{}
`

	ix, _ := indexBoundResults(t, []fixtureFile{
		// Consumer FIRST: at the moment its declaration is indexed, no
		// declaration in dir:provider has been seen yet.
		{path: "consumer/c.go", src: consumerSrc},
		{path: "provider/p.go", src: providerSrc},
	})

	spec := recFor(t, ix, "consumer/c.go:Contract.Take")
	assert.Equal(t, "(dir:provider"+sigKeyLeafSep+"Payload)(ext:error)", spec.SigKey,
		"a leaf naming a package indexed AFTER this declaration still resolves scope-qualified — "+
			"the key is rendered once the index is complete, never during the build")
}
