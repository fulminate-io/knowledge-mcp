// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterfaceQualifierBindsToMethodSpec pins the contract-node model on the
// resolution ladder: a call through an interface-typed qualifier targets the
// INTERFACE METHOD DECLARATION, and the implementers are one IMPLEMENTS hop
// away rather than a fan-out of call edges.
//
// IT IS A CHARACTERIZATION-AND-CONSEQUENCE TEST, NOT A RED-FIRST REPRODUCTION,
// and the distinction is worth stating because nobody re-runs a before-state. No
// production code was written to make it pass. It follows entirely from
// interface method specs being chunked and indexed under Parent=<Interface>: the
// typed rung's existing lookup then finds one, with no interface detection
// anywhere in the ladder. It would be red only in a world where those specs were
// never indexed.
func TestInterfaceQualifierBindsToMethodSpec(t *testing.T) {
	const src = `package svc

type Result struct{}

type Sink interface {
	WriteResult(r Result) error
}

type fileSink struct{}

func (f *fileSink) WriteResult(r Result) error { return nil }

func drive(s Sink, r Result) {
	s.WriteResult(r)
}
`
	ix, e := resolveFixtureRef(t,
		[]fixtureFile{{path: "svc/svc.go", src: src}},
		"svc/svc.go", treesitter.EdgeCalls, "s.WriteResult")

	require.NotNil(t, e.Ref)
	// CONTROLS. Without these the subtests below could pass for reasons that
	// have nothing to do with the interface: an unrecorded qualifier, or an
	// unindexed scope.
	require.Equal(t, treesitter.QualType{Text: "Sink"}, e.Ref.QualifierTypes["s"],
		"control: `s` is recorded as a Sink, so the rung really is being asked about an interface")
	require.True(t, ix.hasScope("dir:svc"), "control: the declaring scope is indexed")

	got := resolveRef(ix, e.Ref, e.ToID)

	t.Run("binds_to_interface_method_node", func(t *testing.T) {
		require.Len(t, got.Candidates, 1,
			"exactly ONE target: the interface's own method declaration")
		assert.Equal(t, "svc/svc.go:Sink.WriteResult", got.Candidates[0].NodeID,
			"the call targets the CONTRACT node, receiver-qualified by the interface")
	})

	t.Run("no_fanout_to_implementers", func(t *testing.T) {
		// The two-hop model made executable: one edge to the interface, and the
		// concrete implementer reachable over IMPLEMENTS rather than by widening
		// this call into a candidate set.
		for _, c := range got.Candidates {
			assert.NotEqual(t, "svc/svc.go:fileSink.WriteResult", c.NodeID,
				"the concrete implementer is NOT a target of this call")
		}
		// KNOWN-POSITIVE CONTROL: the implementer really is in the index, so the
		// absence above is an exclusion rather than a fixture that never declared
		// it.
		require.Contains(t, ix.byID, "svc/svc.go:fileSink.WriteResult",
			"control: the concrete method IS indexed, so its absence from the candidates is a real exclusion")
	})

	t.Run("rule_is_typed_qualifier", func(t *testing.T) {
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleTypedQualifier, got.Rule,
			"the bind is attributed to the typed-qualifier rung, so a future refactor reaching "+
				"the same target through a different rung is visible rather than silent")
	})
}

// ifaceDispatchFixture declares one method name BOTH on an interface spec and on
// a concrete method in one package, plus a dispatch the typed rung cannot bind
// because the method is promoted from outside the indexed scope.
var ifaceDispatchFixture = []fixtureFile{
	{path: "svc/iface.go", src: "" +
		"package svc\n\ntype Runnable interface{ Run() int }\n"},
	{path: "svc/runner.go", src: "" +
		"package svc\n\ntype Runner struct{ n int }\n\nfunc (r Runner) Run() int {\n\treturn r.n\n}\n"},
	{path: "svc/invoke.go", src: "" +
		"package svc\n\nimport \"example.com/ext\"\n\ntype holder struct{ ext.Base }\n\n" +
		"func Invoke(x holder) int {\n\treturn x.Run()\n}\n"},
}

// TestInterfaceSpecsInDynamicCandidateSets covers the consequence of indexing
// interface method specs that the ticket did not name: the index writes EVERY
// declaration into its by-scope-and-name view, and that view IS the candidate
// set of a dynamic dispatch. So a package declaring an interface grows the
// dispatch candidate set for every method name that interface declares.
//
// IS THAT RIGHT? Argued rather than assumed. A dynamic group is OPEN — the
// referent is one of these candidates OR something no static enumeration can
// reach — and under the contract-node model an interface method IS a legitimate
// dispatch target, so growing an open set is correct. What would NOT be correct
// is a spec DISPLACING a concrete method on a reference that BINDS, or a spec
// appearing in an AMBIGUOUS (closed) group, whose whole claim is that exactly one
// listed candidate is the referent. The three subtests are those three
// propositions.
func TestInterfaceSpecsInDynamicCandidateSets(t *testing.T) {
	t.Run("spec_joins_dynamic_set", func(t *testing.T) {
		res := populateFixture(t, ifaceDispatchFixture)
		got := groupEdgesOf(res, kgtypes.EdgeMethodDynamic)
		require.NotEmpty(t, got, "control: the fixture produced an OPEN group at all")

		targets := map[string]bool{}
		for _, e := range got {
			targets[e.ToId] = true
		}
		assert.True(t, targets["svc/runner.go:Runner.Run"],
			"the concrete method is a dispatch candidate, as it always was")
		assert.True(t, targets["svc/iface.go:Runnable.Run"],
			"and so is the interface's method spec — an open set's members are what the dispatch "+
				"COULD reach, and a contract node is one of them")
		for _, e := range got {
			assert.Equal(t, kgtypes.EdgeMethodDynamic, e.Method,
				"every member of this group is stamped dynamic, never ambiguous-name")
		}
	})

	t.Run("bound_ref_not_displaced", func(t *testing.T) {
		// A typed qualifier on a CONCRETE receiver. The spec must not enter the
		// result: this reference BINDS, and a bind names exactly one referent.
		const src = `package svc

type Runnable interface{ Run() int }

type Runner struct{ n int }

func (r Runner) Run() int { return r.n }

func drive(x Runner) int {
	return x.Run()
}
`
		ix, e := resolveFixtureRef(t,
			[]fixtureFile{{path: "svc/all.go", src: src}},
			"svc/all.go", treesitter.EdgeCalls, "x.Run")
		require.NotNil(t, e.Ref)
		require.Equal(t, treesitter.QualType{Text: "Runner"}, e.Ref.QualifierTypes["x"],
			"control: the qualifier is typed to the CONCRETE type")

		got := resolveRef(ix, e.Ref, e.ToID)
		require.Equal(t, RefBound, got.Status)
		require.Len(t, got.Candidates, 1, "a bind names exactly one referent")
		assert.Equal(t, "svc/all.go:Runner.Run", got.Candidates[0].NodeID,
			"the concrete method, not the interface spec that shares its name")
		// KNOWN-POSITIVE CONTROL: the spec IS indexed, so its absence from the
		// result is an exclusion rather than a fixture that never declared it.
		require.Contains(t, ix.byID, "svc/all.go:Runnable.Run",
			"control: the interface spec is in the index and was passed over")
	})

	t.Run("no_spec_in_closed_group", func(t *testing.T) {
		// ONE fixture producing BOTH kinds, so the absence below is measured in a
		// run that demonstrably makes closed groups.
		files := append(append([]fixtureFile{}, twoHandlers...), ifaceDispatchFixture...)
		res := populateFixture(t, files)

		ambiguous := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		dynamic := groupEdgesOf(res, kgtypes.EdgeMethodDynamic)
		// THE KNOWN-POSITIVE THIS SUBTEST CANNOT DO WITHOUT: "no spec in a closed
		// group" is satisfied for free by a fixture that produces no closed groups
		// at all.
		require.NotEmpty(t, ambiguous, "control: the fixture DOES produce a CLOSED group")
		require.NotEmpty(t, dynamic, "control: and an OPEN one, where the spec is allowed")

		specInDynamic := false
		for _, e := range dynamic {
			if e.ToId == "svc/iface.go:Runnable.Run" {
				specInDynamic = true
			}
		}
		require.True(t, specInDynamic,
			"control: the spec really is a candidate somewhere, so the closed-group check below "+
				"is about PLACEMENT rather than about a spec that never appears at all")

		for _, e := range ambiguous {
			assert.NotEqual(t, "svc/iface.go:Runnable.Run", e.ToId,
				"a CLOSED group claims exactly one listed candidate IS the referent; an interface "+
					"method spec can never be that, so it must never be listed in one")
		}
	})
}
