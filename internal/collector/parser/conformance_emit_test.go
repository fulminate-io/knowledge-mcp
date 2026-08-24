// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestF0DeclaredConformanceEmission covers the emission rules three consumer
// groups assert against.
//
// EVERY COUNTER SUBTEST DRIVES ITS COUNTER NON-ZERO. A counter asserted to be
// zero cannot be told apart from a counter that was never wired, so each
// decline reason here is exercised by a fixture that provokes it.
func TestF0DeclaredConformanceEmission(t *testing.T) {
	t.Run("type_level", func(t *testing.T) {
		const (
			path = "app/models.py"
			sup  = path + ":Greeter"
			sub  = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
			{nodeID: sub, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		edges := emitDeclaredConformanceEdges(ix)
		require.Len(t, edges, 1, "a supertype with no members emits its type-level edge and nothing else")
		require.Equal(t, sup, edges[0].FromId, "direction runs SUPERTYPE outward to subtype")
		require.Equal(t, sub, edges[0].ToId)
		require.Equal(t, string(kgtypes.EdgeImplements), edges[0].Type)
	})

	t.Run("member_level", func(t *testing.T) {
		const (
			path     = "app/svc.py"
			sup      = path + ":Greeter"
			supGreet = path + ":Greeter.greet"
			sub      = path + ":Server"
			subGreet = path + ":Server.greet"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
			{nodeID: supGreet, name: "greet", parent: "Greeter"},
			{nodeID: sub, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
			{nodeID: subGreet, name: "greet", parent: "Server"},
		}})

		edges := emitDeclaredConformanceEdges(ix)
		require.True(t, gateHasEdge(edges, sup, sub), "the type-level edge must still be emitted")
		require.True(t, gateHasEdge(edges, supGreet, subGreet),
			"a supertype member whose name matches exactly one subtype member must pair")
		require.Len(t, edges, 2, "exactly the type-level edge and the one member pair")
	})

	t.Run("module_level_only", func(t *testing.T) {
		// The shape a language takes when its container and its members are the
		// same node kind: the members carry NO container name, so no member
		// pairing can exist and the type-level edge stands alone. That is the
		// complete, correct answer for such a language — not a defect.
		const (
			path = "lib/proto.ex"
			sup  = path + ":Proto"
			sub  = path + ":Impl"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangElixir, decls: []gateDecl{
			{nodeID: sup, name: "Proto", facts: conformFacts(true)},
			{nodeID: path + ":proto_handle", name: "proto_handle"},
			{nodeID: sub, name: "Impl", facts: conformFacts(false, declared("Proto", treesitter.ConformBehaviour))},
			{nodeID: path + ":impl_handle", name: "impl_handle"},
		}})

		edges := emitDeclaredConformanceEdges(ix)
		require.Len(t, edges, 1,
			"members that carry no container name cannot pair, so only the type-level edge may be emitted")
		require.True(t, gateHasEdge(edges, sup, sub))
	})

	t.Run("non_contract_counted", func(t *testing.T) {
		const (
			path = "app/base.py"
			base = path + ":Base"
			derv = path + ":Derived"
			ifc  = path + ":Greeter"
			ok   = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: base, name: "Base", facts: conformFacts(false)},
			{nodeID: derv, name: "Derived", facts: conformFacts(false, declared("Base", treesitter.ConformExtends))},
			// KNOWN-POSITIVE CONTROL in the same index: a contract supertype
			// still emits, so the decline below is selective rather than the
			// emitter producing nothing at all.
			{nodeID: ifc, name: "Greeter", facts: conformFacts(true)},
			{nodeID: ok, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		_, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.NonContract,
			"a supertype resolving to a non-contract declaration must be counted, not silently dropped")
		require.Zero(t, stats.Unresolvable,
			"a supertype that RESOLVED is not unresolvable: folding the two counters makes each unreadable")

		edges := emitDeclaredConformanceEdges(ix)
		require.False(t, gateHasEdge(edges, base, derv),
			"a concrete base class emits nothing: its method IS the callable implementation")
		require.True(t, gateHasEdge(edges, ifc, ok), "control: the contract supertype still emits")
	})

	t.Run("unresolvable_counted", func(t *testing.T) {
		const (
			path = "app/ext.py"
			sub  = path + ":Client"
			ifc  = path + ":Greeter"
			ok   = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sub, name: "Client", facts: conformFacts(false, declared("vendor.NoSuchBase", treesitter.ConformExtends))},
			{nodeID: ifc, name: "Greeter", facts: conformFacts(true)},
			{nodeID: ok, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		_, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.Unresolvable,
			"a supertype naming nothing in this repository must be counted: the graph shows the same nothing for that and for declaring no supertype at all")
		require.Zero(t, stats.NonContract,
			"a supertype that never resolved is not a non-contract: the two counters describe opposite situations")

		edges := emitDeclaredConformanceEdges(ix)
		require.Len(t, edges, 1, "only the control pair may emit")
		require.True(t, gateHasEdge(edges, ifc, ok), "control: the resolvable supertype still emits")
	})

	t.Run("ambiguous_supertype_declines", func(t *testing.T) {
		// TWO declarations share one lookup key — the collision-suffixed second
		// spelling keeps its own identity but keys under the same base name — so
		// the supertype spelling is genuinely unknown.
		const (
			path = "app/dup.py"
			sup1 = path + ":Greeter"
			sup2 = path + ":Greeter#a1b2c3d4"
			sub  = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup1, name: "Greeter", facts: conformFacts(true)},
			{nodeID: sup2, name: "Greeter#a1b2c3d4", facts: conformFacts(true)},
			{nodeID: sub, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		_, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.AmbiguousSupertype,
			"a supertype resolving to more than one declaration must be counted under its OWN counter")
		require.Zero(t, stats.AmbiguousMember,
			"the member counter must stay untouched here, or the two ambiguity rules are indistinguishable")

		require.Empty(t, emitDeclaredConformanceEdges(ix),
			"an ambiguous supertype emits NOTHING at either level: taking the head is a wrong-target generator")
	})

	t.Run("ambiguous_member_declines", func(t *testing.T) {
		// The supertype resolves to exactly one contract, so the TYPE-LEVEL edge
		// stands. One member name matches two records on the subtype side, so
		// THAT PAIR alone declines while an unambiguous sibling still pairs.
		const (
			path     = "app/api.py"
			sup      = path + ":Greeter"
			supGreet = path + ":Greeter.greet"
			supPing  = path + ":Greeter.ping"
			sub      = path + ":Server"
			subGreet = path + ":Server.greet"
			subDup   = path + ":Server.greet#c0ffee01"
			subPing  = path + ":Server.ping"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
			{nodeID: supGreet, name: "greet", parent: "Greeter"},
			{nodeID: supPing, name: "ping", parent: "Greeter"},
			{nodeID: sub, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
			{nodeID: subGreet, name: "greet", parent: "Server"},
			{nodeID: subDup, name: "greet#c0ffee01", parent: "Server"},
			{nodeID: subPing, name: "ping", parent: "Server"},
		}})

		_, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.AmbiguousMember,
			"an overloaded member pairing must be counted under its OWN counter")
		require.Zero(t, stats.AmbiguousSupertype,
			"the supertype counter must stay untouched here, or the two ambiguity rules are indistinguishable")

		edges := emitDeclaredConformanceEdges(ix)
		require.True(t, gateHasEdge(edges, sup, sub),
			"the TYPE-LEVEL edge survives an ambiguous member: only the overloaded pair declines")
		require.True(t, gateHasEdge(edges, supPing, subPing),
			"control: the unambiguous sibling member still pairs, so the decline is selective")
		require.False(t, gateHasEdge(edges, supGreet, subGreet),
			"the overloaded member must not pair with an arbitrarily chosen record")
		require.Len(t, edges, 2, "exactly the type-level edge and the one unambiguous member pair")
	})

	t.Run("kind_and_direction_survive", func(t *testing.T) {
		// A NON-DEFAULT kind, so an emitter that hard-coded the first vocabulary
		// member or an empty string cannot satisfy this.
		const (
			path    = "app/mixin.py"
			sup     = path + ":Loggable"
			supLog  = path + ":Loggable.log"
			sub     = path + ":Worker"
			subLog  = path + ":Worker.log"
			wantSfx = "mixin"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup, name: "Loggable", facts: conformFacts(true)},
			{nodeID: supLog, name: "log", parent: "Loggable"},
			{nodeID: sub, name: "Worker", facts: conformFacts(false, declared("Loggable", treesitter.ConformMixin))},
			{nodeID: subLog, name: "log", parent: "Worker"},
		}})

		edges := emitDeclaredConformanceEdges(ix)
		typeEdge := gateFindEdge(t, edges, sup, sub)
		require.Equal(t, sup, typeEdge.FromId,
			"FromId is the SUPERTYPE: the source writes the subtype first, and the edge runs the other way")
		require.Equal(t, sub, typeEdge.ToId)
		require.Equal(t, kgtypes.EdgeMethodDeclaredConformance+wantSfx, typeEdge.Method,
			"Method carries the DECLARED clause kind, never a fabricated method-set count")

		memberEdge := gateFindEdge(t, edges, supLog, subLog)
		require.Equal(t, typeEdge.Method, memberEdge.Method,
			"the member-level edge must carry its type-level parent's Method byte-for-byte")
	})
}
