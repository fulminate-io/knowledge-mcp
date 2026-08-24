// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// A SWIFT PROTOCOL CARRYING AN EXTENSION IS THE IDIOMATIC DEFAULT-IMPLEMENTATION
// SHAPE, and these fixtures pin what it yields. The protocol and its extension
// share one declaration key — a collision suffix is IDENTITY and never KEY — so
// a supertype spelling naming the protocol reaches the resolver with TWO
// candidates, one layer above member pairing.
//
// The fixture paths are LOCKED for the reason the sibling swift fixtures' are:
// swift's resolution unit is the module, derived from the last `Sources` or
// `Tests` segment plus the directory after it, and a fixture written anywhere
// else silently falls back to FILE scope.
const (
	swiftProtoExtProtoFile  = "testdata/f2corpus/swift/Sources/ProtoExt/greeter.swift"
	swiftProtoExtServerFile = "testdata/f2corpus/swift/Sources/ProtoExt/server.swift"
	swiftProtoExtOtherFile  = "testdata/f2corpus/swift/Sources/ProtoExt/other.swift"

	// The CONTAINER half of the protocol's node IDs, without the path-hash
	// suffix that disambiguates the protocol from its extension. It is a PREFIX
	// and never a whole ID: with the extension present both `Greeter`
	// containers and both `greet` members collide and take a suffix, so a
	// constant spelling a whole ID would match nothing.
	swiftProtoExtGreeterBase = swiftProtoExtProtoFile + ":Greeter"

	swiftProtoExtServerType  = swiftProtoExtServerFile + ":Server"
	swiftProtoExtServerGreet = swiftProtoExtServerFile + ":Server.greet"
)

// swiftProtoExtGreeterWithExt is the subject: one requirement, and an extension
// supplying its default implementation.
const swiftProtoExtGreeterWithExt = `public protocol Greeter {
    func greet()
}

extension Greeter {
    public func greet() {}
}
`

// swiftProtoExtGreeterNoExt is the same protocol with the extension removed.
const swiftProtoExtGreeterNoExt = `public protocol Greeter {
    func greet()
}
`

// swiftProtoExtServer conforms the protocol and implements its requirement. It
// declares no extension of its own, so its own node IDs carry no suffix.
const swiftProtoExtServer = `public class Server: Greeter {
    public func greet() {}
}
`

func TestSwiftProtocolExtensionConformance(t *testing.T) {
	t.Run("pairs_through_the_extension", func(t *testing.T) {
		res := populateFixture(t, swiftProtoExtFixtures(swiftProtoExtGreeterWithExt))
		proto := swiftProtoExtProtocolID(t, res)

		typeEdges := declaredEdgesFrom(res, proto)
		require.Lenf(t, typeEdges, 1,
			"the protocol conforms exactly one class, got %v", edgeTargets(typeEdges))
		assert.Equal(t, swiftProtoExtServerType, typeEdges[0].ToId)
		assert.Equal(t, kgtypes.EdgeMethodDeclaredConformance+string(treesitter.ConformUndeclared), typeEdges[0].Method,
			"swift's inheritance_specifier is identical for a superclass and a protocol, so the recorded kind is undeclared rather than a guess")

		memberEdges := swiftProtoExtMemberEdges(res)
		require.Lenf(t, memberEdges, 1,
			"the requirement must pair with the conformer's implementation, got %v", edgeSources(memberEdges))
		assert.Equal(t, typeEdges[0].Method, memberEdges[0].Method,
			"the member edge carries its type-level parent's Method BYTE-FOR-BYTE")
	})

	t.Run("no_double_pair", func(t *testing.T) {
		// THE NEWLY-LIVE BOUNDARY. A protocol extension supplies DEFAULT
		// IMPLEMENTATIONS of the very requirements the protocol declares, so
		// both `greet` records sit under one member key. Exactly one of them is
		// the spec side of the conformance, and it is the protocol's own
		// requirement — not the extension's default.
		res := populateFixture(t, swiftProtoExtFixtures(swiftProtoExtGreeterWithExt))
		proto := swiftProtoExtProtocolID(t, res)

		memberEdges := swiftProtoExtMemberEdges(res)
		require.Lenf(t, memberEdges, 1,
			"exactly one member relationship lands on the conformer's method, got %v", edgeSources(memberEdges))
		// Ownership is read from the containment edge rather than from the node
		// ID, because both `greet` node IDs carry the same base name and differ
		// only in a path-hash suffix.
		assert.Equal(t, proto, swiftProtoExtOwnerOf(t, res, memberEdges[0].FromId),
			"the paired requirement belongs to the PROTOCOL; the extension's default implementation is not a requirement and must not pair")
	})

	t.Run("control_without_extension", func(t *testing.T) {
		// A CHARACTERIZATION GUARD, GREEN BEFORE AND AFTER — never a red-first
		// case. Without the extension the protocol's key holds one declaration,
		// the supertype resolves as it always has, and both edges are emitted.
		// Its job is to say that a red above is the ambiguity firing rather than
		// a fixture that failed to chunk.
		res := populateFixture(t, swiftProtoExtFixtures(swiftProtoExtGreeterNoExt))
		proto := swiftProtoExtProtocolID(t, res)

		typeEdges := declaredEdgesFrom(res, proto)
		require.Lenf(t, typeEdges, 1,
			"the protocol conforms exactly one class, got %v", edgeTargets(typeEdges))
		assert.Equal(t, swiftProtoExtServerType, typeEdges[0].ToId)

		memberEdges := swiftProtoExtMemberEdges(res)
		require.Lenf(t, memberEdges, 1,
			"the requirement pairs with the conformer's implementation, got %v", edgeSources(memberEdges))
	})

	t.Run("counters", func(t *testing.T) {
		// THE EDGES ABOVE SAY WHAT WAS EMITTED; THESE SAY BY WHICH ROUTE. A
		// supertype that resolved without ever colliding would produce the same
		// two edges, so without ReopenedSupertype the subtests above cannot
		// distinguish the narrowing doing its work from the collision never
		// having happened.
		stats := conformanceStatsFor(t, swiftProtoExtFixtures(swiftProtoExtGreeterWithExt))
		assert.Equal(t, 1, stats.ReopenedSupertype,
			"the collided set was narrowed to the contract its extension reopens")
		assert.Equal(t, 0, stats.AmbiguousSupertype,
			"nothing is left ambiguous: the narrowing consumed the one collision")
		assert.Equal(t, 1, stats.TypePairs, "one type-level relationship")
		assert.Equal(t, 1, stats.MemberPairs,
			"one member relationship — the protocol's requirement, not the extension's default")
	})
}

// TestConformanceStatsMirrorsProduction is the NAMED CATCHER for the stats
// harness agreeing with the code it exists to measure.
//
// conformanceStatsFor builds its own index, and production's Populate stamps
// every record's declaration OWNER onto that index before deriving. Any outcome
// depending on ownership therefore disagrees with production unless the harness
// stamps them too — and member pairing depends on ownership on BOTH sides.
//
// THE FIXTURE IS THE ONE SHAPE THAT DISTINGUISHES THEM: the conformance is
// declared on the class and the requirement implemented in its extension, so the
// member lookup must cross from one container to the other, which it can only do
// by reading owners. Production emits the member edge either way; drop the
// stamping from the harness and this reads MemberPairs=0 against a fully correct
// implementation.
func TestConformanceStatsMirrorsProduction(t *testing.T) {
	stats := conformanceStatsFor(t, swiftExtFixtures(`public class Server: Greeter {
}

extension Server {
    public func greet() {}
}
`))
	require.Positive(t, stats.Supertypes,
		"control: the derivation saw a declared supertype, so the count below is a real measurement rather than an empty walk")
	require.Equal(t, 1, stats.TypePairs,
		"control: the type-level pair formed, so a missing member pair below is a PAIRING failure rather than an unresolved supertype")
	assert.Equal(t, 1, stats.MemberPairs,
		"the harness must report the member pair production emits: without the owner stamping it reads 0 here")
}

// TestSwiftSupertypeNarrowingAbstains pins the two collided shapes a narrowing
// rule must NOT resolve.
//
// BOTH ARE CHARACTERIZATION GUARDS, GREEN BEFORE AND AFTER — neither is a
// red-first case. Their whole value is that they turn RED if the rule is ever
// loosened into "any single contract wins": measured during planning, that
// looser rule changes a pinned scala corpus by 766 narrowings, none of which
// this codebase has any evidence for.
//
// THE TWO SUBTESTS COVER DIFFERENT BRANCHES and both are required — one has a
// candidate that is not a reopening at all, the other has more than one
// contract. A single fixture leaves one branch untested.
//
// EACH ZERO IS PAIRED WITH A KNOWN-POSITIVE CONTROL. "No edges" is also what an
// empty walk prints, so every subtest requires the derivation to have SEEN a
// declared supertype before reading anything into what it declined.
func TestSwiftSupertypeNarrowingAbstains(t *testing.T) {
	t.Run("non_reopening_collider", func(t *testing.T) {
		// A function and a type may share a name in swift, so this is legal
		// source. The function is not a container of a reopenable type, so it
		// carries no PartialBody flag and is NOT a reopening of the protocol —
		// the candidate set holds something the contract does not account for
		// and the referent stays genuinely unknown.
		files := swiftProtoExtFixtures(swiftProtoExtGreeterWithExt + `
public func Greeter() {}
`)
		stats := conformanceStatsFor(t, files)
		require.Positive(t, stats.Supertypes,
			"control: the derivation saw a declared supertype, so the counts below are declines rather than an empty walk")
		assert.Equal(t, 1, stats.AmbiguousSupertype,
			"a collider that is not a reopening keeps the supertype ambiguous")

		res := populateFixture(t, files)
		assert.Empty(t, swiftProtoExtMemberEdges(res),
			"an ambiguous supertype emits nothing at either level")
	})

	t.Run("two_contracts", func(t *testing.T) {
		// NOT A VALID SINGLE SWIFT PROGRAM — it is the INDEX STATE a real tree
		// produces when one library is vendored twice or a source is generated
		// into two places, which this codebase already names as a live collision
		// cause. Two contracts means the referent is genuinely unknown: neither
		// is a reopening of the other, and preferring either would manufacture a
		// wrong target.
		files := append(swiftProtoExtFixtures(swiftProtoExtGreeterNoExt),
			fixtureFile{path: swiftProtoExtOtherFile, src: swiftProtoExtGreeterNoExt})
		stats := conformanceStatsFor(t, files)
		require.Positive(t, stats.Supertypes,
			"control: the derivation saw a declared supertype, so the counts below are declines rather than an empty walk")
		assert.Equal(t, 1, stats.AmbiguousSupertype,
			"two same-named contracts keep the supertype ambiguous")

		res := populateFixture(t, files)
		assert.Empty(t, swiftProtoExtMemberEdges(res),
			"an ambiguous supertype emits nothing at either level")
	})
}

// swiftProtoExtFixtures pairs one greeter file with the shared conformer.
func swiftProtoExtFixtures(greeter string) []fixtureFile {
	return []fixtureFile{
		{path: swiftProtoExtProtoFile, src: greeter},
		{path: swiftProtoExtServerFile, src: swiftProtoExtServer},
	}
}

// swiftProtoExtProtocolID returns the node ID of the ONE protocol declaration in
// the result.
//
// THE PROTOCOL IS IDENTIFIED BY NODE TYPE, NEVER BY A SPELLED ID. With the
// extension present both `Greeter` containers collide and are disambiguated by a
// path-hash suffix, so neither carries the bare `<file>:Greeter` spelling the
// sibling swift fixtures can use. The extension chunks as a `class_declaration`
// — one node kind serves class, struct, enum, actor and extension in this
// grammar — so the protocol is the only `protocol_declaration` here, and
// requiring exactly one is what keeps that a fact rather than an assumption.
func swiftProtoExtProtocolID(t *testing.T, res PopulateResult) string {
	t.Helper()
	var found []string
	for _, n := range res.Nodes {
		if n.Type == "protocol_declaration" {
			found = append(found, n.Id)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one protocol declaration node, got %v", found)
	return found[0]
}

// swiftProtoExtMemberEdges returns every declared-conformance relationship
// landing on the conformer's method.
//
// IT SELECTS BY TARGET RATHER THAN BY SOURCE, which is what lets it count a
// DOUBLE pairing: the protocol's requirement and the extension's default are two
// different source nodes, and both would land here.
func swiftProtoExtMemberEdges(res PopulateResult) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements || e.ToId != swiftProtoExtServerGreet {
			continue
		}
		if strings.HasPrefix(e.Method, kgtypes.EdgeMethodDeclaredConformance) {
			out = append(out, e)
		}
	}
	return out
}

// swiftProtoExtOwnerOf returns the declaration that CONTAINS a node, skipping the
// file-level containment every chunk also carries.
func swiftProtoExtOwnerOf(t *testing.T, res PopulateResult, nodeID string) string {
	t.Helper()
	var found []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains || e.ToId != nodeID {
			continue
		}
		if strings.HasPrefix(e.FromId, swiftProtoExtGreeterBase) {
			found = append(found, e.FromId)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one container of %q under %q, got %v", nodeID, swiftProtoExtGreeterBase, found)
	return found[0]
}

// edgeSources renders an edge slice's sources for a failure message, the
// counterpart of edgeTargets for assertions selecting edges by their target.
func edgeSources(edges []*knowledgev1.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.FromId)
	}
	return out
}
