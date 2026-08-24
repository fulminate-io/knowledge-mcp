// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	cppBaseFile   = "src/base.h"
	cppServerFile = "src/server.cc"

	cppGreeter     = cppBaseFile + ":Greeter"
	cppGreeterSpec = cppBaseFile + ":Greeter.greet"
	cppMixin       = cppBaseFile + ":Mixin"
	cppMixinSpec   = cppBaseFile + ":Mixin.mix"
	cppConcrete    = cppBaseFile + ":Concrete"
	cppServer      = cppServerFile + ":Server"
	cppServerGreet = cppServerFile + ":Server.greet"
	cppServerMix   = cppServerFile + ":Server.mix"
	cppChild       = cppServerFile + ":Child"
	cppDrive       = cppServerFile + ":drive"
)

// cppDeclaredFixtures uses the DEFINING C++ LAYOUT — contracts in a header,
// the conformer in a translation unit that includes it — so every conformance
// here crosses a file boundary and resolves through the include binds.
//
// base.h is routed to cpp by the header FALLBACK rather than by its extension:
// extMap sends every `.h` to C first, and the cpp grammar is adopted because
// the C parse of a class with a pure-virtual member produces an error tree.
//
// `seed` calling the free function `mk` is what the bind-only subtest rests on:
// every other call here needs a typed qualifier, so without a plain call the
// unarmed run binds nothing at all and the comparison would have nothing to
// compare.
func cppDeclaredFixtures() []fixtureFile {
	return []fixtureFile{
		{path: cppBaseFile, src: `class Greeter {
 public:
  virtual void greet(Req r) = 0;
};

class Mixin {
 public:
  virtual void mix() = 0;
};

class Concrete {
 public:
  void plain();
};
`},
		{path: cppServerFile, src: `#include "base.h"

class Server : public Greeter, private Mixin {
 public:
  void greet(Req r) override;
  void mix() override;
};

class Child : public Concrete {
 public:
  void go();
};

void drive(Greeter* g, Req r) {
  g->greet(r);
}

Req mk() {
  return Req();
}

Req seed() {
  return mk();
}
`},
	}
}

// TestCPPDeclaredImplements exercises the cpp chunker arms, the declaration
// index, the include-bind scope and the declared-conformance emission path
// together.
func TestCPPDeclaredImplements(t *testing.T) {
	res := populateFixture(t, cppDeclaredFixtures())

	t.Run("abstract", func(t *testing.T) {
		// A base with a pure-virtual member is the only thing C++ offers as a
		// structural contract signal, and it is what makes the base-class
		// relationship worth an IMPLEMENTS edge.
		assert.Truef(t, hasEdge(res, kgtypes.EdgeImplements, cppGreeter, cppServer),
			"no IMPLEMENTS edge %s -> %s", cppGreeter, cppServer)

		edges := declaredEdgesFrom(res, cppGreeter)
		require.Len(t, edges, 1, "the abstract base conforms exactly the one class that names it")
		assert.Equal(t, kgtypes.EdgeMethodDeclaredConformance+string(treesitter.ConformUndeclared), edges[0].Method,
			"C++ has no keyword saying whether a base is a contract, so the recorded kind is undeclared rather than a guess")
	})

	t.Run("concrete", func(t *testing.T) {
		// `class Child : public Concrete` is a base-class relationship the
		// source declared, and it is CAPTURED — but Concrete declares no pure
		// virtual, so it is not a contract and the pair emits nothing.
		assert.Empty(t, declaredEdgesFrom(res, cppConcrete),
			"a base resolving to a NON-CONTRACT declaration emits nothing: a concrete base's method IS the callable implementation")
		assert.Empty(t, declaredEdgesFrom(res, cppChild))

		stats := conformanceStatsFor(t, cppDeclaredFixtures())
		require.Positive(t, stats.Supertypes,
			"control: the derivation saw declared bases, so the counts below are declines rather than an empty walk")
		assert.Positive(t, stats.NonContract, "Concrete has no pure-virtual member, which is the NonContract outcome")
		assert.Zero(t, stats.Unresolvable,
			"every base here names an in-repo declaration, so NonContract and Unresolvable are demonstrably SEPARATE counters")
	})

	t.Run("access", func(t *testing.T) {
		// `private Mixin` is still a DECLARED base-class relationship. The
		// access specifier is dropped from the recorded spelling and gates
		// nothing, so a private abstract base emits exactly as a public one
		// does.
		assert.Truef(t, hasEdge(res, kgtypes.EdgeImplements, cppMixin, cppServer),
			"no IMPLEMENTS edge %s -> %s: private inheritance is still a declared base-class relationship", cppMixin, cppServer)
		assert.Truef(t, hasEdge(res, kgtypes.EdgeImplements, cppMixinSpec, cppServerMix),
			"the member pairing follows the type-level edge regardless of the access specifier")
	})

	t.Run("method_edge", func(t *testing.T) {
		// THE IMPLEMENTER SIDE ONLY EXISTS BECAUSE OF THIS PHASE'S CAPTURE
		// FOLD-IN. `void greet(Req r) override;` declared in a class body is a
		// field_declaration over a function_declarator, which no landed
		// TopLevel row matched — so before that row this edge had no target at
		// all.
		specEdges := declaredEdgesFrom(res, cppGreeterSpec)
		require.Lenf(t, specEdges, 1, "the base's pure-virtual member must pair with the derived class's member, got %v", edgeTargets(specEdges))
		assert.Equal(t, cppServerGreet, specEdges[0].ToId)

		parent := edgeWithTarget(declaredEdgesFrom(res, cppGreeter), cppServer)
		require.NotNil(t, parent, "control: the type-level edge exists, or the byte comparison below has nothing to compare against")
		assert.Equal(t, parent.Method, specEdges[0].Method,
			"the member edge carries its type-level parent's Method BYTE-FOR-BYTE")
	})

	t.Run("two_hop", func(t *testing.T) {
		// The target is asserted BY EQUALITY and the candidate count by LENGTH.
		// A containment assertion would pass while a fan-out silently included
		// the right answer among many wrong ones.
		targets := edgesFrom(res, kgtypes.EdgeCalls, cppDrive)
		require.Lenf(t, targets, 1,
			"a call through an abstract-base pointer resolves to ONE target, not a fan-out group, got %v", targets)
		assert.Equal(t, cppGreeterSpec, targets[0],
			"the call targets the BASE's member; the implementers are one IMPLEMENTS hop away")
	})

	t.Run("bind_only", func(t *testing.T) {
		// THE NO-REGRESSION GUARANTEE AS AN ASSERTION. The armed run may bind
		// MORE, and it may narrow an ambiguous fan-out group; it may never
		// re-point a reference that was already resolved to a single target.
		armed := boundCallTargets(res)

		treesitter.UnregisterQualifierTypes(treesitter.LangCPP)
		treesitter.UnregisterTypeFacts(treesitter.LangCPP)
		// RESTORING BY RE-REGISTERING, never by deleting: an unregistered
		// production arm silently disarms the feature for every later test in
		// the same binary.
		t.Cleanup(func() {
			treesitter.RegisterCPPQualifierTypes()
			treesitter.RegisterCPPTypeFacts()
		})
		unarmed := boundCallTargets(populateFixture(t, cppDeclaredFixtures()))

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
