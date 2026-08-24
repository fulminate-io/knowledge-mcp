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

// rustDeclaredFixtures is the fixture set every subtest below runs over.
//
// IT IS SAME-FILE BY CHOICE RATHER THAN BY NECESSITY. Rust resolves declared
// supertypes across files through the import-bind rung, so a cross-file layout
// would work — but this test's subject is the two-hop CALLS targeting through
// an already-indexed trait method spec, and a same-file fixture exercises that
// rung without dragging bind resolution into the same assertion. The cross-file
// case is the corpus audit's, where the bind mechanism is the subject.
//
// external.rs is a SEPARATE file deliberately: its assertion is an absence plus
// a counter, and neither depends on where the declaration lives.
//
// `seed` calling the free function `mk` is what the bind-only subtest rests on:
// every OTHER call here is a method call that resolves only once a qualifier
// carries a type, so without a plain call the unarmed run binds nothing at all
// and the comparison would have nothing to compare.
func rustDeclaredFixtures() []fixtureFile {
	return []fixtureFile{
		{path: "src/greeter.rs", src: `pub struct Req;
pub struct Resp;
pub struct Server;

pub fn mk() -> Req {
    Req
}

pub trait Greeter {
    fn greet(&self, r: Req) -> Resp;
}

impl Greeter for Server {
    fn greet(&self, r: Req) -> Resp {
        r.take()
    }
}

impl Server {
    fn helper(&self, r: Req) {
        self.greet(r);
    }
}

fn drive(g: &dyn Greeter, r: Req) {
    g.greet(r);
}

fn seed() -> Req {
    mk()
}
`},
		{path: "src/external.rs", src: `pub struct Other;

impl std::fmt::Display for Other {
}
`},
	}
}

const (
	rustTrait     = "src/greeter.rs:Greeter"
	rustTraitSpec = "src/greeter.rs:Greeter.greet"
	rustImplBase  = "src/greeter.rs:Server"
	rustImplGreet = "src/greeter.rs:Server.greet"
	rustDrive     = "src/greeter.rs:drive"
)

// TestRustDeclaredImplements is the only place the rust chunker arms, the
// declaration index and the declared-conformance emission path are exercised
// together. Everything before it tests one layer.
func TestRustDeclaredImplements(t *testing.T) {
	res := populateFixture(t, rustDeclaredFixtures())

	// THE IMPL BLOCK'S NODE ID CARRIES A COLLISION SUFFIX AND IS DISCOVERED
	// RATHER THAN SPELLED. `pub struct Server`, `impl Greeter for Server` and
	// `impl Server` are three declarations of one name in one file, so the
	// chunker disambiguates all three with an AST-path hash. Hard-coding the
	// hash would tie this test to the fixture's byte layout; reading it back
	// asserts the same thing without that coupling.
	implEdges := declaredEdgesFrom(res, rustTrait)

	t.Run("type_edge", func(t *testing.T) {
		require.Lenf(t, implEdges, 1,
			"the trait must contribute exactly one type-level IMPLEMENTS edge, got %v", implEdges)
		assert.Truef(t, strings.HasPrefix(implEdges[0].ToId, rustImplBase+"#"),
			"the type-level edge must land on the impl block that names the trait, got %q", implEdges[0].ToId)
		assert.Equal(t, kgtypes.EdgeMethodDeclaredConformance+string(treesitter.ConformTrait), implEdges[0].Method,
			"rust is the one language here whose grammar names its clause, so the kind is trait rather than undeclared")
	})

	t.Run("method_edge", func(t *testing.T) {
		// This is what proves the trait's method spec became a NODE. Without
		// it the type-level edge alone would pass while trait methods stayed
		// invisible to every consumer.
		specEdges := declaredEdgesFrom(res, rustTraitSpec)
		require.Lenf(t, specEdges, 1,
			"the trait's method spec must pair with the impl's method, got %v", specEdges)
		assert.Equal(t, rustImplGreet, specEdges[0].ToId)
		require.Len(t, implEdges, 1, "control: the type-level edge exists, or the byte comparison below has nothing to compare against")
		assert.Equal(t, implEdges[0].Method, specEdges[0].Method,
			"the member edge carries its type-level parent's Method BYTE-FOR-BYTE")
	})

	t.Run("two_hop", func(t *testing.T) {
		// The target is asserted BY EQUALITY and the candidate count by LENGTH.
		// A containment assertion would pass while a fan-out silently included
		// the right answer among many wrong ones.
		targets := edgesFrom(res, kgtypes.EdgeCalls, rustDrive)
		require.Lenf(t, targets, 1,
			"a call through a trait-typed parameter resolves to ONE target, not a fan-out group, got %v", targets)
		assert.Equal(t, rustTraitSpec, targets[0],
			"the call targets the trait's method spec; the implementers are one IMPLEMENTS hop away")
	})

	t.Run("inherent", func(t *testing.T) {
		// KNOWN-NEGATIVE CONTROL over real output: the fixture declares TWO
		// impl blocks for Server and only the one naming a trait may emit.
		var implNodes []string
		for _, n := range res.Nodes {
			if n.Type == "impl_item" && strings.HasPrefix(n.Id, rustImplBase+"#") {
				implNodes = append(implNodes, n.Id)
			}
		}
		require.Lenf(t, implNodes, 2,
			"control: the fixture must present two impl blocks for Server, or 'only one emitted' is satisfied by there being only one, got %v", implNodes)

		emitting := map[string]bool{}
		for _, e := range res.Edges {
			if kgtypes.EdgeType(e.Type) == kgtypes.EdgeImplements {
				emitting[e.ToId] = true
			}
		}
		hit := 0
		for _, id := range implNodes {
			if emitting[id] {
				hit++
			}
		}
		assert.Equal(t, 1, hit, "an inherent impl declares no conformance and must contribute no IMPLEMENTS edge")
	})

	t.Run("external", func(t *testing.T) {
		for _, e := range res.Edges {
			if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
				continue
			}
			assert.NotContainsf(t, e.FromId, "src/external.rs", "an out-of-repo supertype emits nothing: %s -> %s", e.FromId, e.ToId)
			assert.NotContainsf(t, e.ToId, "src/external.rs", "an out-of-repo supertype emits nothing: %s -> %s", e.FromId, e.ToId)
		}

		// A COUNTER THAT MUST BE NON-ZERO NEEDS A CASE THAT DRIVES IT, or it
		// cannot be told apart from a counter that was never wired. The index
		// is rebuilt here through the same production helpers populate uses,
		// because the emitter logs its stats rather than returning them.
		stats := conformanceStatsFor(t, rustDeclaredFixtures())
		require.Positive(t, stats.Supertypes,
			"control: the derivation saw at least one declared supertype, so the count below is a decline rather than an empty walk")
		assert.Positive(t, stats.Unresolvable,
			"`impl std::fmt::Display for Other` names a supertype this repository does not declare, which is the Unresolvable outcome")
		assert.Zero(t, stats.NonContract,
			"no fixture here resolves a supertype to a concrete declaration, so NonContract stays separate and empty")
	})

	t.Run("bind_only", func(t *testing.T) {
		// THE NO-REGRESSION GUARANTEE AS AN ASSERTION. The armed run may bind
		// MORE, and it may narrow an ambiguous fan-out group; it may never
		// re-point a reference that was already resolved to a single target.
		armed := boundCallTargets(res)

		treesitter.UnregisterQualifierTypes(treesitter.LangRust)
		treesitter.UnregisterTypeFacts(treesitter.LangRust)
		// RESTORING BY RE-REGISTERING, never by deleting: an unregistered
		// production arm silently disarms the feature for every later test in
		// the same binary, which is the hazard the registry's own doc names.
		t.Cleanup(func() {
			treesitter.RegisterRustQualifierTypes()
			treesitter.RegisterRustTypeFacts()
		})
		unarmedRes := populateFixture(t, rustDeclaredFixtures())
		unarmed := boundCallTargets(unarmedRes)

		require.NotEmpty(t, unarmed,
			"control: the unarmed run resolved at least one call to a single target, or the comparison below is vacuous")
		for key := range unarmed {
			assert.Containsf(t, armed, key,
				"a call the unarmed run had already bound must keep the identical target once the arms register: %s", key)
		}
		// KNOWN-POSITIVE CONTROL for the arm having done anything at all: the
		// armed run binds strictly more than the unarmed one. Without it, an
		// arm that returned nil for everything would satisfy every assertion
		// above.
		assert.Greater(t, len(armed), len(unarmed),
			"the armed run must bind MORE than the unarmed one, or this subtest proves only that nothing changed")
	})
}

// conformanceStatsFor returns what the declared-conformance derivation declined
// over one fixture set.
//
// IT REBUILDS THE INDEX THROUGH THE PRODUCTION PREPARATION STEPS rather than
// reusing the lighter indexResults helper, and the difference is load-bearing:
// chunkResultsToPopulate runs fillBinds before the index build, and a supertype
// spelled in one file and declared in another resolves ONLY through those
// binds. Skipping the pass reports every cross-file supertype as unresolvable —
// measured on the cpp fixtures, three of them — which would make a decline
// count look like a real one.
//
// It exists at all because the emitter LOGS its stats rather than returning
// them, so a subtest asserting a decline reason has no other way to observe it.
func conformanceStatsFor(t *testing.T, files []fixtureFile) conformanceStats {
	t.Helper()
	results := chunkFixture(t, files)
	rc := &treesitter.RepoContext{ModulePath: "example.com/fixture"}
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	fillBinds(rc, results)

	total := 0
	for _, r := range results {
		total += len(r.Chunks)
	}
	ix := newDeclIndex(total)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
		}
	}
	ix.resolveSigKeys()
	// THE OWNER STAMPING IS PART OF THE PREPARATION, NOT AN EXTRA. Populate runs
	// it in exactly this position, and member pairing reads owners on both
	// sides — so a harness that skipped it would report MemberPairs=0 for every
	// pairing that crosses between two containers of one type, against a fully
	// correct implementation. TestConformanceStatsMirrorsProduction is the
	// catcher: remove this line and it goes red.
	stampDeclOwners(ix, results)
	_, stats := deriveDeclaredConformance(ix)
	return stats
}

// declaredEdgesFrom returns the declared-conformance IMPLEMENTS edges leaving
// one node, in emission order.
func declaredEdgesFrom(res PopulateResult, from string) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeImplements && e.FromId == from {
			out = append(out, e)
		}
	}
	return out
}

// boundCallTargets returns the "from -> to" key of every CALLS edge resolved to
// a SINGLE target.
//
// THE ZERO CONFIDENCE IS THE DISCRIMINATOR, and it is what makes the bind-only
// comparison meaningful: a reference resolved to a single candidate carries the
// zero value, while a fan-out group divides its confidence across its members.
// Comparing every edge regardless would treat NARROWING a group — the whole
// point of the typed-qualifier rung — as a broken binding.
func boundCallTargets(res PopulateResult) map[string]bool {
	out := map[string]bool{}
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls && e.Confidence == 0 {
			out[e.FromId+" -> "+e.ToId] = true
		}
	}
	return out
}
