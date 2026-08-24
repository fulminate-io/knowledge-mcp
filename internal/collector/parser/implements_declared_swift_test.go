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

// THE FIXTURE PATHS ARE LOCKED, NOT INCIDENTAL. Swift's resolution unit is the
// build MODULE, derived from the source tree's layout: the last `Sources` or
// `Tests` segment plus the directory after it. A fixture written anywhere else
// falls back to FILE scope, the cross_file subtest below would then be
// asserting a same-file relationship under a cross-file name, and it would pass.
const (
	swiftProtoFile  = "testdata/f2corpus/swift/Sources/Greeting/protocol.swift"
	swiftServerFile = "testdata/f2corpus/swift/Sources/Greeting/server.swift"

	swiftGreeter     = swiftProtoFile + ":Greeter"
	swiftGreeterSpec = swiftProtoFile + ":Greeter.greet"
	swiftChatty      = swiftProtoFile + ":Chatty"
	swiftBase        = swiftProtoFile + ":Base"
	swiftServerBase  = swiftServerFile + ":Server"
	swiftServerGreet = swiftServerFile + ":Server.greet"
	swiftDrive       = swiftServerFile + ":drive"
)

// swiftDeclaredFixtures declares the protocol, its refinement and a concrete
// base class in ONE file, and the conforming class, its extension and two
// callers in ANOTHER — so every conformance here crosses a file boundary and
// resolves only through the module scope.
//
// `seed` calling the free function `mk` is what the bind-only subtest rests on:
// every other call here needs a typed qualifier, so without a plain call the
// unarmed run binds nothing at all and the comparison would have nothing to
// compare.
func swiftDeclaredFixtures() []fixtureFile {
	return []fixtureFile{
		{path: swiftProtoFile, src: `public protocol Greeter {
    func greet(r: Req) -> Resp
}

public protocol Chatty: Greeter {
}

public class Base {
    public func base() {}
}
`},
		{path: swiftServerFile, src: `public class Server: Base, Greeter {
    public func greet(r: Req) -> Resp {
        return mk()
    }
}

extension Server: Chatty {
    public func extra() {}
}

func drive(g: Greeter, r: Req) {
    g.greet(r: r)
}

func seed() -> Req {
    return mk()
}

public func mk() -> Req {
    return Req()
}
`},
	}
}

// TestSwiftDeclaredImplements exercises the swift chunker arms, the declaration
// index, the module scope and the declared-conformance emission path together.
func TestSwiftDeclaredImplements(t *testing.T) {
	res := populateFixture(t, swiftDeclaredFixtures())

	// THE CONFORMER'S NODE ID CARRIES A COLLISION SUFFIX AND IS DISCOVERED
	// RATHER THAN SPELLED: `class Server` and `extension Server` are two
	// declarations of one name in one file, so the chunker disambiguates both
	// with an AST-path hash. Reading them back asserts the same thing without
	// tying this test to the fixture's byte layout.
	fromGreeter := declaredEdgesFrom(res, swiftGreeter)
	fromChatty := declaredEdgesFrom(res, swiftChatty)

	t.Run("proto", func(t *testing.T) {
		require.Lenf(t, fromGreeter, 2,
			"the protocol conforms one refinement and one class, got %v", edgeTargets(fromGreeter))
		target := typeLevelTarget(t, fromGreeter)
		assert.Equal(t, kgtypes.EdgeMethodDeclaredConformance+string(treesitter.ConformUndeclared), edgeWithTarget(fromGreeter, target).Method,
			"swift's inheritance_specifier is identical for a superclass and a protocol, so the recorded kind is undeclared rather than a guess")
	})

	t.Run("superclass", func(t *testing.T) {
		// `class Server: Base, Greeter` names TWO supertypes in one clause and
		// the parse tree cannot tell them apart. Base resolves to a concrete
		// class, so it emits NOTHING and is counted separately from a supertype
		// this repository does not contain at all.
		assert.Empty(t, declaredEdgesFrom(res, swiftBase),
			"a supertype resolving to a NON-CONTRACT declaration emits nothing")

		stats := conformanceStatsFor(t, swiftDeclaredFixtures())
		require.Positive(t, stats.Supertypes,
			"control: the derivation saw declared supertypes, so the counts below are declines rather than an empty walk")
		assert.Positive(t, stats.NonContract,
			"Base is a concrete class, which is the NonContract outcome")
		assert.Zero(t, stats.Unresolvable,
			"every supertype here names an in-repo declaration, so NonContract and Unresolvable are demonstrably SEPARATE counters rather than one folded total")
	})

	t.Run("extension", func(t *testing.T) {
		// An extension carries its own inheritance clause, and it is a
		// DIFFERENT declaration from the class it extends — so the two
		// conformances must land on two different nodes.
		require.Lenf(t, fromChatty, 1, "the refinement conforms the extension, got %v", edgeTargets(fromChatty))
		ext := typeLevelTarget(t, fromChatty)
		cls := typeLevelTarget(t, fromGreeter)
		assert.NotEqual(t, cls, ext,
			"the extension is its own declaration: a conformance declared on it must not be attributed to the class body")
	})

	t.Run("refine", func(t *testing.T) {
		// A protocol refining another is a conformance like any other, and
		// dropping it would lose the only relationship a protocol hierarchy has.
		assert.Truef(t, hasEdge(res, kgtypes.EdgeImplements, swiftGreeter, swiftChatty),
			"no IMPLEMENTS edge %s -> %s: a protocol's own inheritance clause is recorded too", swiftGreeter, swiftChatty)
	})

	t.Run("cross_file", func(t *testing.T) {
		// THE CATCHER FOR THE WHOLE MODULE-SCOPE CONSUMPTION. Every other
		// subtest here passes on a same-file tree, so without this one the
		// cross-file claim goes untested. The two endpoint FILE PATHS are
		// compared directly rather than an edge count being checked, because a
		// count is satisfied by the same-file case alone.
		target := typeLevelTarget(t, fromGreeter)
		assert.Equal(t, swiftProtoFile, nodeFile(swiftGreeter))
		assert.Equal(t, swiftServerFile, nodeFile(target))
		assert.NotEqual(t, nodeFile(swiftGreeter), nodeFile(target),
			"the conformance's two endpoints must live in DIFFERENT files, which resolves only through the swift module scope")
	})

	t.Run("method_edge", func(t *testing.T) {
		// This is what proves the protocol_function_declaration query row
		// actually produced a NODE. Until it existed the row was verified only
		// by a query-TEXT grep.
		specEdges := declaredEdgesFrom(res, swiftGreeterSpec)
		require.Lenf(t, specEdges, 1, "the protocol's method spec must pair with the conformer's method, got %v", edgeTargets(specEdges))
		assert.Equal(t, swiftServerGreet, specEdges[0].ToId)

		parent := edgeWithTarget(fromGreeter, typeLevelTarget(t, fromGreeter))
		require.NotNil(t, parent, "control: the type-level edge exists, or the byte comparison below has nothing to compare against")
		assert.Equal(t, parent.Method, specEdges[0].Method,
			"the member edge carries its type-level parent's Method BYTE-FOR-BYTE")
	})

	t.Run("two_hop", func(t *testing.T) {
		// The target is asserted BY EQUALITY and the candidate count by LENGTH.
		// A containment assertion would pass while a fan-out silently included
		// the right answer among many wrong ones.
		targets := edgesFrom(res, kgtypes.EdgeCalls, swiftDrive)
		require.Lenf(t, targets, 1,
			"a call through a protocol-typed parameter resolves to ONE target, not a fan-out group, got %v", targets)
		assert.Equal(t, swiftGreeterSpec, targets[0],
			"the call targets the protocol's method spec; the conformers are one IMPLEMENTS hop away")
	})

	t.Run("bind_only", func(t *testing.T) {
		// THE NO-REGRESSION GUARANTEE AS AN ASSERTION. The armed run may bind
		// MORE, and it may narrow an ambiguous fan-out group; it may never
		// re-point a reference that was already resolved to a single target.
		armed := boundCallTargets(res)

		treesitter.UnregisterQualifierTypes(treesitter.LangSwift)
		treesitter.UnregisterTypeFacts(treesitter.LangSwift)
		// RESTORING BY RE-REGISTERING, never by deleting: an unregistered
		// production arm silently disarms the feature for every later test in
		// the same binary.
		t.Cleanup(func() {
			treesitter.RegisterSwiftQualifierTypes()
			treesitter.RegisterSwiftTypeFacts()
		})
		unarmed := boundCallTargets(populateFixture(t, swiftDeclaredFixtures()))

		require.NotEmpty(t, unarmed,
			"control: the unarmed run resolved at least one call to a single target, or the comparison below is vacuous")
		for key := range unarmed {
			assert.Containsf(t, armed, key,
				"a call the unarmed run had already bound must keep the identical target once the arms register: %s", key)
		}
		assert.Greater(t, len(armed), len(unarmed),
			"the armed run must bind MORE than the unarmed one, or this subtest proves only that nothing changed")
	})
}

// nodeFile returns the file path half of a node ID, which is everything before
// the LAST colon — a path may itself contain none, but a node ID always ends
// with ":<symbol>".
func nodeFile(nodeID string) string {
	if i := strings.LastIndex(nodeID, ":"); i >= 0 {
		return nodeID[:i]
	}
	return nodeID
}

// edgeTargets renders an edge slice's targets for a failure message.
func edgeTargets(edges []*knowledgev1.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.ToId)
	}
	return out
}

// typeLevelTarget returns the single edge target under the conformer's node-ID
// prefix, and fails when the count is not exactly one — so a fan-out is a
// failure rather than a silently-taken first element.
//
// THE PREFIX IS THE CONFORMER'S BASE ID rather than a parameter, because the
// class and its extension both carry it with an AST-path suffix appended: the
// two are distinguished by which edge lands on which, not by two prefixes.
func typeLevelTarget(t *testing.T, edges []*knowledgev1.Edge) string {
	t.Helper()
	var found []string
	for _, e := range edges {
		if strings.HasPrefix(e.ToId, swiftServerBase) {
			found = append(found, e.ToId)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one edge target under %q, got %v", swiftServerBase, found)
	return found[0]
}

// edgeWithTarget returns the edge landing on one target, or nil.
func edgeWithTarget(edges []*knowledgev1.Edge, target string) *knowledgev1.Edge {
	for _, e := range edges {
		if e.ToId == target {
			return e
		}
	}
	return nil
}
