// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// A RUST INHERENT IMPL IS NOT ANOTHER BODY OF THE TRAIT IMPL, and this fixture
// is the guard on that. The shape it pins is legal, idiomatic rust that a
// member-ownership regression would silently mispair: a trait whose method
// carries a DEFAULT body, an EMPTY `impl Trait for Type` that therefore needs
// to declare nothing, and an INHERENT `impl Type` declaring a method of the
// same name. The trait requirement is satisfied by its own default, and the
// inherent method is a DIFFERENT method that merely shares a spelling — so the
// requirement must pair with NOTHING here.
//
// WHAT IT GUARDS AGAINST is an owner-filter regression re-pairing the inherent
// method: every member is indexed under {Scope, Parent, Name} with Parent the
// container's BASE name, and both impl blocks are written for `Server`, so the
// trait impl and the inherent impl land under one member key and are told apart
// only by which container OWNS each candidate. Weaken or drop that ownership
// narrowing — or extend co-ownership to a language where two same-named
// containers are two different entities — and the trait's requirement pairs
// with the inherent method, which is an edge asserting a conformance the source
// never wrote.
func TestRustInherentMethodDoesNotSatisfyDefaultedRequirement(t *testing.T) {
	const mispairFile = "src/mispair.rs"
	const (
		mispairTrait       = mispairFile + ":Greeter"
		mispairDefaulted   = mispairFile + ":Greeter.greet"
		mispairRequired    = mispairFile + ":Greeter.shout"
		mispairImplBase    = mispairFile + ":Server"
		mispairInherentFn  = mispairFile + ":Server.greet"
		mispairConformedFn = mispairFile + ":Server.shout"
	)

	// `shout` IS THE KNOWN-POSITIVE CONTROL AND IT LIVES IN THE SAME FIXTURE.
	// The subject assertion below is an ABSENCE, which a run that paired no
	// members at all would satisfy just as well as a correct one; a second
	// requirement the trait impl really does declare is what tells those apart.
	res := populateFixture(t, []fixtureFile{{path: mispairFile, src: `pub struct Server;

pub trait Greeter {
    fn greet(&self) {}
    fn shout(&self);
}

impl Greeter for Server {
    fn shout(&self) {}
}

impl Server {
    fn greet(&self) {}
}
`}})

	typeEdges := declaredEdgesFrom(res, mispairTrait)
	require.Lenf(t, typeEdges, 1,
		"control: the trait must contribute exactly one type-level edge, or a missing member edge below "+
			"would mean the conformance was never recorded rather than correctly declined, got %v",
		edgeTargets(typeEdges))
	conformer := typeEdges[0].ToId
	require.Truef(t, strings.HasPrefix(conformer, mispairImplBase+"#"),
		"control: the type-level edge lands on the impl block naming the trait, got %q", conformer)

	// CONTROL THAT THE FIXTURE REALLY PROVOKES THE COLLISION. The inherent
	// method must exist and must be owned by a DIFFERENT container than the
	// conforming one — otherwise there is no candidate to mispair and the
	// absence asserted below holds for a reason this test does not test.
	inherentOwner := rustMispairContainerOf(t, res, mispairImplBase, mispairInherentFn)
	require.Truef(t, strings.HasPrefix(inherentOwner, mispairImplBase+"#"),
		"control: the inherent method is contained by an impl block for Server, got %q", inherentOwner)
	require.NotEqual(t, conformer, inherentOwner,
		"control: the inherent impl and the trait impl are DIFFERENT declarations sharing one member key, "+
			"which is the condition the ownership narrowing exists to resolve")

	assert.Truef(t, sameContainerBase(inherentOwner, rustMispairContainerOf(t, res, mispairImplBase, mispairConformedFn)),
		"control: both impl blocks are written for the same type, so their members really do share a key")

	// THE CONTROL FIRES: a requirement the trait impl DOES declare still pairs.
	controlEdges := declaredEdgesFrom(res, mispairRequired)
	require.Lenf(t, controlEdges, 1,
		"control: `shout` is declared by the conforming impl and must still pair, got %v", edgeTargets(controlEdges))
	assert.Equal(t, mispairConformedFn, controlEdges[0].ToId)

	// THE SUBJECT. `greet` is satisfied by the trait's own default body and the
	// conforming impl declares no `greet` at all; the only declaration under that
	// member key belongs to the inherent impl, which is a different method.
	assert.Emptyf(t, edgeTargets(declaredEdgesFrom(res, mispairDefaulted)),
		"a defaulted requirement whose conforming impl declares nothing must pair with nothing: the "+
			"inherent method of the same name is a DIFFERENT method, and pairing to it asserts a "+
			"conformance the source never wrote")
}

// rustMispairContainerOf returns the DECLARATION that contains one member,
// failing unless there is exactly one. Ownership is read from the containment
// edge rather than from the node ID because a member's ID carries only its
// container's BASE name, and the two impl blocks here are indistinguishable by
// that name — which is the whole point of the fixture.
//
// The candidates are narrowed to containers under `base`, because every chunk
// also carries a containment edge from its FILE: file-to-symbol containment is
// addressed positionally and is emitted for every chunk, so an unfiltered walk
// returns the file alongside the declaration.
func rustMispairContainerOf(t *testing.T, res PopulateResult, base, member string) string {
	t.Helper()
	var found []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains || e.ToId != member {
			continue
		}
		if strings.HasPrefix(e.FromId, base) {
			found = append(found, e.FromId)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one declaration container of %q, got %v", member, found)
	return found[0]
}

// sameContainerBase reports whether two container node IDs name one type,
// comparing the spelling with any AST-path collision suffix removed.
func sameContainerBase(a, b string) bool {
	base := func(id string) string {
		trimmed, _, _ := strings.Cut(id, "#")
		return trimmed
	}
	return base(a) == base(b)
}
