// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// A SWIFT TYPE AND ITS EXTENSIONS ARE ONE NOMINAL TYPE, AND THESE FIXTURES PIN
// THAT — a conformance declared on either half is satisfied by a requirement
// implemented in either half, so all three shapes below must produce the member
// relationship. They are a GUARD ON MEMBER-OWNERSHIP NARROWING, not just
// coverage: a member is indexed under {Scope, Parent, Name} with Parent a BASE
// name, and a class and its extension share that key by construction — both
// chunk as `Server`, disambiguated only by an AST-path suffix the index strips.
// Any future rule that resolves that shared key by keeping only the members a
// container OWNS will delete the correct edges in `requirement_in_extension` and
// `retroactive_conformance`, because ownership and conformance sit on opposite
// halves of one type there. That rule is right for languages where two
// same-named containers are two different entities; swift is the language where
// they are not, and these three subtests are where the difference shows up.
//
// The fixture paths are LOCKED for the same reason the sibling swift test's
// are: swift's resolution unit is the module, derived from the last `Sources`
// or `Tests` segment plus the directory after it, and a fixture written
// anywhere else silently falls back to FILE scope.
const (
	swiftExtProtoFile  = "testdata/f2corpus/swift/Sources/Extending/greeter.swift"
	swiftExtServerFile = "testdata/f2corpus/swift/Sources/Extending/server.swift"

	swiftExtGreeter     = swiftExtProtoFile + ":Greeter"
	swiftExtGreeterSpec = swiftExtProtoFile + ":Greeter.greet"
	swiftExtServerBase  = swiftExtServerFile + ":Server"
	swiftExtServerGreet = swiftExtServerFile + ":Server.greet"
)

// swiftExtProtocol is the one protocol every subtest here conforms: a single
// requirement, so a missing member relationship is a missing edge rather than a
// smaller count.
const swiftExtProtocol = `public protocol Greeter {
    func greet()
}
`

func TestSwiftExtensionMemberPairing(t *testing.T) {
	t.Run("requirement_in_extension", func(t *testing.T) {
		// Conformance on the CLASS, requirement in the EXTENSION. The class body
		// declares nothing at all, which is what makes the member lookup cross
		// from one container to the other.
		res := populateFixture(t, swiftExtFixtures(`public class Server: Greeter {
}

extension Server {
    public func greet() {}
}
`))
		conformer, owner := swiftExtConformer(t, res), swiftExtOwnerOfGreet(t, res)
		assert.NotEqual(t, conformer, owner,
			"the conformance is declared on the class and the requirement implemented in the extension: this subtest is only meaningful while those are DIFFERENT declarations")
		swiftExtRequireMemberEdge(t, res)
	})

	t.Run("declared_on_extension", func(t *testing.T) {
		// Retroactive conformance, requirement in the same extension — the one
		// shape where the conforming declaration also owns the member.
		res := populateFixture(t, swiftExtFixtures(`public class Server {
}

extension Server: Greeter {
    public func greet() {}
}
`))
		conformer, owner := swiftExtConformer(t, res), swiftExtOwnerOfGreet(t, res)
		assert.Equal(t, conformer, owner,
			"conformance and requirement are both declared on the extension, so one declaration is both the conformer and the member's owner")
		swiftExtRequireMemberEdge(t, res)
	})

	t.Run("retroactive_conformance", func(t *testing.T) {
		// Conformance on the EXTENSION, requirement already in the CLASS — the
		// idiomatic way to declare that an existing type already satisfies a
		// protocol. The crossing runs the opposite way from the first subtest.
		res := populateFixture(t, swiftExtFixtures(`public class Server {
    public func greet() {}
}

extension Server: Greeter {
}
`))
		conformer, owner := swiftExtConformer(t, res), swiftExtOwnerOfGreet(t, res)
		assert.NotEqual(t, conformer, owner,
			"the conformance is declared on the extension and the requirement implemented in the class: this subtest is only meaningful while those are DIFFERENT declarations")
		swiftExtRequireMemberEdge(t, res)
	})
}

// swiftExtFixtures pairs the shared protocol with one conformer file.
func swiftExtFixtures(server string) []fixtureFile {
	return []fixtureFile{
		{path: swiftExtProtoFile, src: swiftExtProtocol},
		{path: swiftExtServerFile, src: server},
	}
}

// swiftExtRequireMemberEdge asserts the requirement pairs with its
// implementation, and asserts the TYPE-LEVEL edge first as the control: without
// it a missing member edge could equally mean the whole conformance was never
// recorded, which is a different failure with a different repair.
func swiftExtRequireMemberEdge(t *testing.T, res PopulateResult) {
	t.Helper()
	require.NotEmpty(t, declaredEdgesFrom(res, swiftExtGreeter),
		"control: the type-level conformance was recorded, so a missing member edge below is a PAIRING failure rather than an empty walk")

	specEdges := declaredEdgesFrom(res, swiftExtGreeterSpec)
	require.Lenf(t, specEdges, 1,
		"the protocol's requirement must pair with exactly one implementation, got %v", edgeTargets(specEdges))
	assert.Equal(t, swiftExtServerGreet, specEdges[0].ToId)
}

// swiftExtConformer returns the declaration the type-level conformance landed
// on, failing unless there is exactly one — a fan-out is a failure here rather
// than a silently-taken first element.
func swiftExtConformer(t *testing.T, res PopulateResult) string {
	t.Helper()
	var found []string
	for _, e := range declaredEdgesFrom(res, swiftExtGreeter) {
		if strings.HasPrefix(e.ToId, swiftExtServerBase) {
			found = append(found, e.ToId)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one conformance target under %q, got %v", swiftExtServerBase, found)
	return found[0]
}

// swiftExtOwnerOfGreet returns the declaration that CONTAINS the implementation.
// Ownership is read from the containment edge rather than from the node ID
// because the ID carries only the container's base name — the two halves of the
// type are indistinguishable by name, which is the whole point of these
// fixtures.
func swiftExtOwnerOfGreet(t *testing.T, res PopulateResult) string {
	t.Helper()
	var found []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains || e.ToId != swiftExtServerGreet {
			continue
		}
		if strings.HasPrefix(e.FromId, swiftExtServerBase) {
			found = append(found, e.FromId)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one container of %q, got %v", swiftExtServerGreet, found)
	return found[0]
}
