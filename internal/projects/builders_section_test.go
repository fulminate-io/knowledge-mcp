// SPDX-License-Identifier: Apache-2.0

package projects

// builders_section_test.go covers the chunked-plan section subtree: one
// plan_section node per part, joined to the root by a POSITIONED contains edge,
// with no section body text on the root and no depends-on edge anywhere.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

func sectionedPlanArgs() PlanArgs {
	return PlanArgs{
		Name:    "chunked plan",
		Goal:    "the goal, the tree stamp and the reads",
		Summary: "a sectioned plan",
		Sections: []SectionArgs{
			{Name: "Touch points", Body: "BODY-ZERO every site the change reaches", Summary: "touch points"},
			{Name: "Reuse", Body: "BODY-ONE the exact symbol to extend", Summary: "reuse"},
			{Name: "What to test", Body: "BODY-TWO the list the implementer writes tests from", Summary: "what to test"},
		},
	}
}

// edgePosition reads the position off a batch edge's Evidence, reporting whether
// one is carried.
func edgePosition(t *testing.T, e kgwire.BatchEdge) (int, bool) {
	t.Helper()
	if e.Evidence == "" {
		return 0, false
	}
	var body struct {
		Position string `json:"position"`
	}
	require.NoError(t, json.Unmarshal([]byte(e.Evidence), &body), "edge evidence must be the {\"position\":\"N\"} shape")
	pos, err := strconv.Atoi(body.Position)
	require.NoError(t, err)
	return pos, true
}

// R1-a. Every root→section edge is `contains` carrying Evidence {"position":"i"}
// for i in 0..N-1, and every section node mirrors its edge's position on its own
// metadata. Observed on the built batch, which is what PersistBatch sends.
func TestBuildPlanGraph_SectionsCarryPositionsOnBothCarriers(t *testing.T) {
	nodes, edges, err := BuildPlanGraph(sectionedPlanArgs(), nil, nil)
	require.NoError(t, err)

	var sectionIdx []int
	for i, n := range nodes {
		if n.Type == string(kgtypes.NodePlanSection) {
			sectionIdx = append(sectionIdx, i)
		}
	}
	require.Len(t, sectionIdx, 3, "one plan_section node per section")

	seen := map[int]string{}
	for _, e := range edges {
		if e.FromIdx != 0 || e.ToIdx < 0 {
			continue
		}
		if nodes[e.ToIdx].Type != string(kgtypes.NodePlanSection) {
			continue
		}
		// THE EDGE TYPE IS THE LOWERCASE knowledge `contains`, never the
		// uppercase code-graph CONTAINS: the subtree traversal every tree render
		// rides asks for the lowercase one, so the uppercase would make every
		// section invisible in every tree while still persisting.
		assert.Equal(t, kgtypes.EdgeKGContains, e.Type)
		assert.Equal(t, string(kgtypes.EdgeKGContains), "contains", "the wire literal is lowercase")

		pos, ok := edgePosition(t, e)
		require.True(t, ok, "every root→section edge carries a position on its evidence")
		seen[pos] = nodes[e.ToIdx].SymbolName

		// The node carrier mirrors the edge carrier.
		assert.Equal(t, strconv.Itoa(pos), kgtypes.Value(nodes[e.ToIdx], "position"),
			"the section node mirrors its edge's position")
	}
	assert.Equal(t, map[int]string{0: "Touch points", 1: "Reuse", 2: "What to test"}, seen,
		"positions are 0..N-1 in the caller's order")

	// The parentless ROOT carries no position.
	assert.Empty(t, kgtypes.Value(nodes[0], "position"), "the root is parentless and carries no position")
}

// R1-b. The root's description carries the goal and NO section body text, with a
// SAME-RUN CONTROL through the same instrument so "the search found nothing"
// stays distinguishable from "the search did not run".
func TestBuildPlanGraph_RootCarriesNoSectionBody(t *testing.T) {
	args := sectionedPlanArgs()
	nodes, _, err := BuildPlanGraph(args, nil, nil)
	require.NoError(t, err)
	root := nodes[0]

	require.Equal(t, string(kgtypes.NodePlan), root.Type)
	assert.Equal(t, args.Goal, root.Description, "the root's description is the goal, unchanged")

	byName := map[string]*knowledgev1.Node{}
	for _, n := range nodes {
		if n.Type == string(kgtypes.NodePlanSection) {
			byName[n.SymbolName] = n
		}
	}
	for _, s := range args.Sections {
		assert.NotContains(t, root.Description, s.Body, "no section body text is on the root")
		// CONTROL, same instrument, same run: the section's OWN description does
		// contain its body, so NotContains above is a real absence.
		require.Contains(t, byName[s.Name].Description, s.Body,
			"control: the section node's description carries its own body")
	}
}

// R1-f. A sectioned plan carries ZERO depends-on edges, asserted on the built
// EDGE SET rather than on a render.
//
// WHY THIS IS ITS OWN TEST. appendPhaseSubtree chains phases with a depends-on
// edge and appendStepSubtree chains steps with another; a section builder that
// copied either line would silently OVERRIDE every position, because the tree
// renderer's topological sort runs after the child index is read and a
// depends-on chain outranks it. There is no error and no tell — the tree simply
// renders in a different order.
func TestBuildPlanGraph_SectionsEmitNoDependsOnEdge(t *testing.T) {
	_, edges, err := BuildPlanGraph(sectionedPlanArgs(), nil, nil)
	require.NoError(t, err)

	for i, e := range edges {
		assert.NotEqual(t, kgtypes.EdgeDependsOn, e.Type,
			"edges[%d] is a depends-on edge; a depends-on chain overrides every section position in the tree render", i)
	}

	// CONTROL through the same instrument: a PHASE plan does emit depends-on
	// edges, so the absence above is a property of the section builder rather
	// than of the assertion.
	_, phaseEdges, perr := BuildPlanGraph(PlanArgs{
		Name: "phase plan", Goal: "g", Summary: "s",
		Phases: []PhaseArgs{{Name: "one"}, {Name: "two"}},
	}, nil, nil)
	require.NoError(t, perr)
	dependsOn := 0
	for _, e := range phaseEdges {
		if e.Type == kgtypes.EdgeDependsOn {
			dependsOn++
		}
	}
	assert.Positive(t, dependsOn, "control: a phase plan chains its phases with depends-on")
}

// A plan with phases and no sections builds exactly as it always has: no
// plan_section node, no positioned edge.
func TestBuildPlanGraph_PhasePlanIsUnchanged(t *testing.T) {
	nodes, edges, err := BuildPlanGraph(PlanArgs{
		Name: "phase plan", Goal: "g", Summary: "s",
		Phases: []PhaseArgs{{Name: "one", Steps: []StepArgs{{Name: "s1"}}}},
	}, nil, nil)
	require.NoError(t, err)

	for _, n := range nodes {
		assert.NotEqual(t, string(kgtypes.NodePlanSection), n.Type)
	}
	for _, e := range edges {
		assert.Empty(t, e.Evidence, "a phase plan's edges carry no position evidence")
	}
}

// Explicit positions, when the caller supplies them, are used verbatim — and a
// HOLE in the sequence is legal, because closing it would mean rewriting every
// later section, which is the whole-plan rewrite the chunked shape exists to
// remove.
func TestBuildPlanGraph_ExplicitPositionsAreUsedVerbatim(t *testing.T) {
	pos := func(i int) *int { return &i }
	nodes, edges, err := BuildPlanGraph(PlanArgs{
		Name: "holes", Goal: "g", Summary: "s",
		Sections: []SectionArgs{
			{Name: "a", Body: "A", Position: pos(0)},
			{Name: "b", Body: "B", Position: pos(5)},
		},
	}, nil, nil)
	require.NoError(t, err)

	got := map[string]string{}
	for _, e := range edges {
		if e.ToIdx >= 0 && nodes[e.ToIdx].Type == string(kgtypes.NodePlanSection) {
			p, ok := edgePosition(t, e)
			require.True(t, ok)
			got[nodes[e.ToIdx].SymbolName] = strconv.Itoa(p)
		}
	}
	assert.Equal(t, map[string]string{"a": "0", "b": "5"}, got,
		"a gap in the position sequence is preserved, not compacted")
}

// TestBuildPlanGraph_PhasePlanGraphIsByteIdenticalToPreChange is the other half
// of R5-b: not only the rendered result but the PERSISTED node and edge set of a
// phase plan is byte-identical to what the pre-change tree built.
//
// WHY BOTH HALVES. The render is what a caller sees; the graph is what is stored
// and what every later read walks. A change could leave the render identical and
// still move an edge's direction, add a metadata key, or reorder the batch so the
// slot indices an edge refers to point somewhere else — none of which the render
// would show, and all of which would break a plan written afterwards.
//
// THE GOLDEN IS CAPTURED FROM THE PRE-CHANGE TREE, origin/main 46196268, by
// running this exact fixture through BuildPlanGraph there and transcribing that
// run's output. It is not composed here: a composed expectation asserts only that
// this test agrees with itself.
//
// IT RENDERS THE SLOT INDICES, not resolved ids, because the indices ARE the
// contract at this layer — PersistBatch resolves them afterwards, and an edge
// pointing at the wrong slot is exactly the regression a byte compare over
// resolved ids would hide.
func TestBuildPlanGraph_PhasePlanGraphIsByteIdenticalToPreChange(t *testing.T) {
	const preChangeGraph = "node[0] type=plan name=\"phase plan\" desc=\"g\" sum=\"s\" content=\"\" status=\"active\" source=\"llm:claude\" meta=map[no_patterns_reason:trivial]\n" +
		"node[1] type=phase name=\"phase-1\" desc=\"o\" sum=\"ps\" content=\"\" status=\"pending\" source=\"llm:claude\" meta=map[]\n" +
		"node[2] type=step name=\"step-1\" desc=\"step 1 description body\" sum=\"ss\" content=\"\" status=\"pending\" source=\"llm:claude\" meta=map[]\n" +
		"node[3] type=criterion name=\"c\" desc=\"c\" sum=\"cs\" content=\"\" status=\"\" source=\"llm:claude\" meta=map[command:go test ./... type:automated]\n" +
		"edge[0] from=0 to=1 fromID=\"\" toID=\"\" type=contains method=\"\" evidence=\"\" weight=0 confidence=0\n" +
		"edge[1] from=1 to=2 fromID=\"\" toID=\"\" type=contains method=\"\" evidence=\"\" weight=0 confidence=0\n" +
		"edge[2] from=3 to=2 fromID=\"\" toID=\"\" type=verifies method=\"\" evidence=\"\" weight=0 confidence=0\n" +
		"edge[3] from=2 to=3 fromID=\"\" toID=\"\" type=contains method=\"\" evidence=\"\" weight=0 confidence=0\n"

	plan := PlanArgs{
		Name: "phase plan", Goal: "g", Summary: "s", NoPatternsReason: "trivial",
		Phases: []PhaseArgs{{
			Name: "phase-1", Overview: "o", Summary: "ps",
			Steps: []StepArgs{{
				Name: "step-1", Description: "step 1 description body", Summary: "ss",
				Criteria: []CriterionArgs{{Description: "c", Summary: "cs", Type: "automated", Command: "go test ./..."}},
			}},
		}},
	}
	nodes, edges, err := BuildPlanGraph(plan, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, preChangeGraph, renderPlanGraphForCompare(nodes, edges),
		"a phase plan's persisted node and edge set is byte-identical to the pre-change tree's")
}

// renderPlanGraphForCompare serializes a built plan graph deterministically, in
// the SAME form the pre-change capture used, so the two strings are comparable.
// Go renders a map in sorted key order under %v, so the metadata field is stable
// without sorting it here.
// EVERY FIELD THE BUILDER CAN SET IS IN THE STRING, which is the property that
// makes this a byte compare rather than a compare of the fields I happened to
// think of. The first version omitted Source — a field the builder stamps on
// EVERY node — so changing the plan root's provenance left the comparison green.
// A byte compare over a projection is only as strong as the projection, and a
// field outside it is a field the test silently does not defend. Content on the
// node side and FromID, Weight and Confidence on the edge side were missing for
// the same reason and are here now.
func renderPlanGraphForCompare(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) string {
	var sb strings.Builder
	for i, n := range nodes {
		fmt.Fprintf(&sb, "node[%d] type=%s name=%q desc=%q sum=%q content=%q status=%q source=%q meta=%v\n",
			i, n.Type, n.SymbolName, n.Description, n.Summary, n.Content, n.Status, n.Source, n.Metadata)
	}
	for i, e := range edges {
		fmt.Fprintf(&sb, "edge[%d] from=%d to=%d fromID=%q toID=%q type=%s method=%q evidence=%q weight=%v confidence=%v\n",
			i, e.FromIdx, e.ToIdx, e.FromID, e.ToID, e.Type, e.Method, e.Evidence, e.Weight, e.Confidence)
	}
	return sb.String()
}
