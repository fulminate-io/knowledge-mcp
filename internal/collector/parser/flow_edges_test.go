// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// flowFixture drives every flow shape this ticket emits through the production
// populate path. EACH CLAUSE HAS A SUBTEST IT MAKES FALSIFIABLE:
//
//   - `a := p` then `helper(a)` is the ALIAS CASE at the emission layer. Without
//     it the whole closure engine could be inert and every other subtest here
//     would still pass, because a direct `helper(p)` needs no closure at all.
//   - `missing.Sink(q)` names a callee no file in the fixture declares, which is
//     what drives the unresolved-callee self-edge and the arg_edges_unresolved
//     counter non-zero.
//   - `dup(p)` names a callee declared TWICE in this package, which is what
//     makes an ambiguous group.
//   - `return a` gives the self-edge shape a result position to name.
//   - `s.cache = v` is the receiver-field write, whose owner is a SIBLING type
//     declaration that no slot addresses.
const flowFixture = `package svc

type Server struct {
	cache string
}

func (s *Server) Store(v string) {
	s.cache = v
}

func Handle(p string, q string) string {
	a := p
	helper(a)
	missing.Sink(q)
	dup(p)
	return a
}

func helper(x string) {}
`

// flowDupA and flowDupB declare ONE name twice in one package, which is what
// gives dup(p) an ambiguous candidate set. It is not valid Go to compile, and it
// does not need to be: the collector indexes what the source declares.
const flowDupA = `package svc

func dup(x string) {}
`

const flowDupB = `package svc

func dup(x string) {}
`

// flowFixtureFiles is the fixture set every subtest populates.
func flowFixtureFiles() []fixtureFile {
	return []fixtureFile{
		{path: "svc/flow.go", src: flowFixture},
		{path: "svc/dup_a.go", src: flowDupA},
		{path: "svc/dup_b.go", src: flowDupB},
	}
}

// isFlowEdge reports whether an edge is one of the three flow types.
func isFlowEdge(e *knowledgev1.Edge) bool {
	switch kgtypes.EdgeType(e.Type) {
	case kgtypes.EdgeFlowsToReturn, kgtypes.EdgeFlowsToArg, kgtypes.EdgeFlowsToField:
		return true
	}
	return false
}

// flowEdgesOf populates a fixture set and returns only its flow edges.
func flowEdgesOf(t *testing.T, files []fixtureFile) []*knowledgev1.Edge {
	t.Helper()
	res := populateFixture(t, files)
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if isFlowEdge(e) {
			out = append(out, e)
		}
	}
	require.NotEmpty(t, out, "control: the fixture emitted flow edges at all")
	return out
}

// flowEdgesOfType narrows a flow edge slice to one type.
func flowEdgesOfType(edges []*knowledgev1.Edge, want kgtypes.EdgeType) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) == want {
			out = append(out, e)
		}
	}
	return out
}

// TestFlowEdgeEmission pins the emitted flow edges' endpoints, their Evidence,
// and the identity properties a consumer depends on.
func TestFlowEdgeEmission(t *testing.T) {
	edges := flowEdgesOf(t, flowFixtureFiles())

	t.Run("return_is_self_edge", func(t *testing.T) {
		returns := flowEdgesOfType(edges, kgtypes.EdgeFlowsToReturn)
		require.Len(t, returns, 1, "one parameter reaches one result position")
		e := returns[0]
		assert.Equal(t, "svc/flow.go:Handle", e.FromId)
		assert.Equal(t, e.FromId, e.ToId, "a return fact is a SELF-EDGE on the declaration")
		assert.Equal(t, "flow:p0>r0", e.Evidence,
			"the aliased parameter zero reaches result zero, under the declared grammar")
	})

	t.Run("arg_targets_callee", func(t *testing.T) {
		// THE ALIAS CASE. helper receives `a`, which was bound from `p` — so this
		// edge existing at all is the closure engine working end to end.
		var found *knowledgev1.Edge
		for _, e := range flowEdgesOfType(edges, kgtypes.EdgeFlowsToArg) {
			if e.ToId == "svc/flow.go:helper" {
				found = e
			}
		}
		require.NotNil(t, found,
			"a RESOLVED callee's flow edge names the callee's node id, not its spelling")
		assert.Equal(t, "svc/flow.go:Handle", found.FromId)
		assert.Equal(t, "flow:p0>a0", found.Evidence)
		assert.NotEmpty(t, found.Method,
			"a bound flow edge carries the resolving rung, exactly as a bound CALLS edge does")
	})

	t.Run("unresolved_callee_self_edge", func(t *testing.T) {
		// THE CLASSIFICATION IS STRUCTURAL — Type == FLOWS_TO_ARG && FromId ==
		// ToId — AND NEVER "Evidence contains an @". The @ is an admitted
		// callee-name character in every language, so Ruby's `@logger.info` and
		// C#'s `@class.Method` are ORDINARY RESOLVED callees whose spelling
		// carries one; a textual test misclassifies every one of them.
		var selfEdges, resolved []*knowledgev1.Edge
		for _, e := range flowEdgesOfType(edges, kgtypes.EdgeFlowsToArg) {
			if e.FromId == e.ToId {
				selfEdges = append(selfEdges, e)
				continue
			}
			resolved = append(resolved, e)
		}
		require.Len(t, selfEdges, 1, "the one callee no file declares becomes one self-edge")
		e := selfEdges[0]
		assert.Equal(t, "svc/flow.go:Handle", e.FromId)
		assert.Empty(t, e.Method,
			"NO rung fired, and stamping one would name a resolution that did not happen")
		// The Evidence content is checked SECOND and does not decide the class:
		// what it asserts is that the spelling survived, which is the entire
		// point of keeping the fact.
		assert.Equal(t, "flow:p1>a0@missing.Sink", e.Evidence,
			"the @ component carries the callee spelling as the chunker wrote it")

		require.NotEmpty(t, resolved,
			"known-positive: the resolved calls in the same fixture yield FromId != ToId, so the "+
				"structural test above separates two populations rather than matching everything")
	})

	t.Run("field_targets_owner", func(t *testing.T) {
		fields := flowEdgesOfType(edges, kgtypes.EdgeFlowsToField)
		require.Len(t, fields, 1)
		e := fields[0]
		assert.Equal(t, "svc/flow.go:Server.Store", e.FromId)
		assert.Equal(t, "svc/flow.go:Server", e.ToId,
			"the owner is the RECEIVER TYPE's node — a sibling declaration no slot addresses")
		assert.Equal(t, "flow:p0>f:cache", e.Evidence, "the field name rides Evidence")
	})

	t.Run("no_edge_names_a_missing_node", func(t *testing.T) {
		// THE CATCHER FOR THE nodeIDs GUARD on resolveFlowArg. Without that guard
		// a call inside a declaration populate creates no node for emits an edge
		// naming an id no node carries — twice over on the unresolved branch,
		// where FromId is also ToId — and every other subtest here still passes,
		// because they assert SPECIFIC ids rather than the absence of unknown
		// ones.
		//
		// HONEST LABEL: this is a CHARACTERIZATION GUARD, not a red-first
		// assertion. No Go fixture shape reachable here produces a declaration
		// populate skips — the shapes that do are comment orphans and unnamed
		// declarations, neither of which emits a flow edge — so this subtest
		// cannot be driven red from Go source alone. It guards the guard.
		ids := map[string]bool{}
		res := populateFixture(t, flowFixtureFiles())
		for _, n := range res.Nodes {
			ids[n.Id] = true
		}
		require.NotEmpty(t, ids, "known-positive: the populate pass created nodes at all")
		require.True(t, ids["svc/flow.go:Handle"],
			"known-positive: the id set holds the fixture's own function, so an id set built "+
				"wrong cannot make the loop below vacuous")

		for _, e := range edges {
			assert.True(t, ids[e.FromId], "FromId names no node: %s -> %s", e.FromId, e.ToId)
			assert.True(t, ids[e.ToId], "ToId names no node: %s -> %s", e.FromId, e.ToId)
		}
	})

	t.Run("arg_agrees_with_calls_for_resolved", func(t *testing.T) {
		// SCOPED STRUCTURALLY BY THE SELF-EDGE TEST, not by scanning Evidence for
		// an @: a self-edge to an unresolved spelling has no sibling CALLS edge by
		// construction, while an ambiguous Ruby or C# callee whose OWN spelling
		// carries an @ is exactly the edge most in need of this check.
		//
		// PER-EDGE, NEVER AN AGGREGATE. A count-versus-count assertion is
		// satisfied by redistribution.
		calls := map[[2]string]bool{}
		res := populateFixture(t, flowFixtureFiles())
		for _, e := range res.Edges {
			switch kgtypes.EdgeType(e.Type) {
			case kgtypes.EdgeCalls, kgtypes.EdgeTestCalls:
				calls[[2]string{e.FromId, e.ToId}] = true
			}
		}

		checked := 0
		for _, e := range flowEdgesOfType(edges, kgtypes.EdgeFlowsToArg) {
			if e.FromId == e.ToId {
				continue
			}
			checked++
			assert.True(t, calls[[2]string{e.FromId, e.ToId}],
				"a resolved flow edge has a sibling call edge with the same endpoints: %s -> %s",
				e.FromId, e.ToId)
		}
		require.NotZero(t, checked,
			"known-positive: at least one RESOLVED flow edge was checked, so the loop above did "+
				"not pass over an empty set")
	})

	t.Run("evidence_deterministic", func(t *testing.T) {
		// DETERMINISM MEANS POSITION-INDEPENDENT, AND A SAME-FIXTURE RERUN CANNOT
		// SHOW IT: populating one fixture twice produces identical byte offsets,
		// so a position-derived Evidence key passes that comparison. The real
		// probe shifts every offset in the file while leaving the declarations
		// alone.
		//
		// THE EDIT IS A COMMENT BLOCK SEPARATED FROM THE FIRST DECLARATION BY A
		// BLANK LINE. populate folds a comment IMMEDIATELY preceding a symbol
		// into that symbol's description, so without the blank line the fixture
		// would change the declaration rather than only its offsets.
		const shifted = `package svc

// A block of prose that exists only to move every byte offset below it.
// It says nothing about the declarations that follow, and it is separated
// from the first of them by a blank line so populate does not fold it in.

type Server struct {
	cache string
}

func (s *Server) Store(v string) {
	s.cache = v
}

func Handle(p string, q string) string {
	a := p
	helper(a)
	missing.Sink(q)
	dup(p)
	return a
}

func helper(x string) {}
`
		identity := func(files []fixtureFile) map[[4]string]bool {
			got := flowEdgesOf(t, files)
			out := map[[4]string]bool{}
			for _, e := range got {
				out[[4]string{e.FromId, e.ToId, e.Type, e.Evidence}] = true
			}
			return out
		}

		base := identity(flowFixtureFiles())
		require.NotEmpty(t, base,
			"liveness control: the harness produces a non-empty identity set, so an "+
				"empty-versus-empty comparison cannot pass silently")
		assert.Equal(t, base, identity(flowFixtureFiles()),
			"liveness control: the same fixture twice produces the same set")

		moved := flowFixtureFiles()
		moved[0].src = shifted
		assert.Equal(t, base, identity(moved),
			"every byte offset in the file moved and NOT ONE identity changed — the key is "+
				"derived from the declaration's own shape rather than from a position")

		for _, e := range edges {
			assert.NotEmpty(t, e.Evidence,
				"no flow edge has empty Evidence: %s -> %s (%s)", e.FromId, e.ToId, e.Type)
		}
	})

	t.Run("group_evidence_composes", func(t *testing.T) {
		var group []*knowledgev1.Edge
		for _, e := range flowEdgesOfType(edges, kgtypes.EdgeFlowsToArg) {
			if e.Method == kgtypes.EdgeMethodAmbiguousName {
				group = append(group, e)
			}
		}
		require.Len(t, group, 2, "one call, two same-named candidates, one edge each")
		for _, e := range group {
			assert.InDelta(t, 0.5, e.Confidence, 1e-9, "each member carries Confidence 1/N")
			assert.Contains(t, e.Evidence, "flow:p0>a0|",
				"the flow key survives AND the group key is appended after it — the composition "+
					"groupEdges could not produce, because it sets Evidence to the bare group key")
		}
		assert.Equal(t, group[0].Evidence, group[1].Evidence,
			"the members of one group share one key; that shared key IS the grouping")

		// CONTROL: the exact bind beside it carries no group suffix at all.
		for _, e := range flowEdgesOfType(edges, kgtypes.EdgeFlowsToArg) {
			if e.ToId == "svc/flow.go:helper" {
				assert.NotContains(t, e.Evidence, "|",
					"an exact bind is not a group and carries no group suffix")
			}
		}
	})
}
