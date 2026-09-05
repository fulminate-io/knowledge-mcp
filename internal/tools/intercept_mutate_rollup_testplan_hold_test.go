// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_rollup_testplan_hold_test.go extends the hold rule's coverage
// to the test-plan container shape, and pins the container-type set itself with a
// census so the next container type cannot ship outside the hold.
//
// WHY A SEPARATE FILE FROM intercept_mutate_rollup_hold_test.go: that file's cases
// all root at a plan/phase/step and share rollupHoldFake's plan-shaped fixture.
// These root at a test_plan, which is the shape the rule did not reach.
//
// THREE OF THESE START RED against the unfixed tree:
//   TestPlanRootCascadesAtAll        a test_plan root is not a rollup container, so
//                                    the arm declines and no cascade happens at all
//   TestPlanHoldsUnevaluatedTestStep the held test_step is not held — nothing is
//   ContainerSetCoversEveryCriteriaOwningContainer
//                                    test_plan and test_step are on a builder
//                                    contains-path to a criterion and are not in
//                                    the container set
//
// ONE IS A CHARACTERIZATION GUARD, red before the fix only because the arm does not
// fire at all, and green after:
//   TestPlanCascadesWhenCriteriaEvaluated  the pair to the second test, differing in
//                                          ONE field — the criterion's status — so
//                                          the two together show the rule
//                                          discriminates on whether the criterion was
//                                          evaluated rather than on a test_plan
//                                          merely being the root.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// testPlanHoldFake seeds tp-1 -> {ts-crit, ts-bare} with REAL contains edges, and
// hangs one criterion off ts-crit only. ts-bare carries no criterion, so it is the
// in-fixture control that stops "hold everything" from satisfying the assertions.
// critStatus is the one field the held and cascaded cases differ in.
func testPlanHoldFake(critStatus string) *fakeRollupGraphCaller {
	return &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "tp-1", Type: string(kgtypes.NodeTestPlan)},
		descendants: []knowledgev1.Node{
			{Id: "ts-crit", Type: string(kgtypes.NodeTestStep), Status: "pending"},
			{Id: "ts-bare", Type: string(kgtypes.NodeTestStep), Status: "pending"},
			{Id: "crit-tp", Type: string(kgtypes.NodeCriterion), Status: critStatus},
		},
		structureEdges: []knowledgev1.Edge{
			{FromId: "tp-1", ToId: "ts-crit", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "tp-1", ToId: "ts-bare", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "ts-crit", ToId: "crit-tp", Type: string(kgtypes.EdgeKGContains)},
		},
	}
}

// completeTestPlanOne drives the rollup arm the way a caller does: a single-id
// update of tp-1 to completed, with expand_to_descendants left absent (the default).
func completeTestPlanOne(t *testing.T, fc *fakeRollupGraphCaller) (bool, kgtools.ToolResult) {
	t.Helper()
	return InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"tp-1","status":"completed"}`),
	})
}

// TestInterceptMutate_RollupHold_TestPlanRootCascadesAtAll is the outer half of the
// defect, and it is asserted separately from the hold because the two fail for
// different reasons. Before the fix the rollup arm DECLINES a test_plan root
// outright — cascadeStatusForContainerUpdate consults the same container predicate
// the partitioner does — so the update forwards to the generic engine path as a
// bare single-node write: no cascade, no hold, nothing named. That is the shape the
// live measurement recorded as a bare affected:1.
//
// RED against the unfixed tree, where InterceptMutate does not claim the call.
func TestInterceptMutate_RollupHold_TestPlanRootCascadesAtAll(t *testing.T) {
	fc := testPlanHoldFake("pass")
	handled, res := completeTestPlanOne(t, fc)
	require.True(t, handled,
		"the rollup arm must claim a completed-status test_plan update — declining it is the silent no-cascade close")
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.Contains(t, fc.lastUpdate.GetSelection().GetIds(), "tp-1",
		"the named test_plan takes the caller's status")
}

// TestInterceptMutate_RollupHold_TestPlanHoldsUnevaluatedTestStep is the defect
// itself: ts-crit owns a criterion nobody has marked, so the cascade must stop
// above it while its criterion-free sibling still completes.
//
// RED against the unfixed tree, where no cascade fires for a test_plan root at all.
func TestInterceptMutate_RollupHold_TestPlanHoldsUnevaluatedTestStep(t *testing.T) {
	fc := testPlanHoldFake("pending")
	handled, res := completeTestPlanOne(t, fc)
	require.True(t, handled, "the rollup arm claims a completed-status test_plan update")
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.ElementsMatch(t, []string{"tp-1", "ts-bare"}, fc.lastUpdate.GetSelection().GetIds(),
		"ts-crit owns an unevaluated criterion, so the cascade must not write its status")

	body := toolResultText(res)
	heldAt := strings.Index(body, heldNodesLiteral)
	require.GreaterOrEqual(t, heldAt, 0, "the success line must introduce the held bucket: %s", body)
	assert.Contains(t, body[heldAt:], "ts-crit", "the held test_step must be named to the caller")
	assert.Contains(t, body[heldAt:], "("+string(kgtypes.NodeTestStep)+")",
		"the held node is named with its type, which is what makes the enumeration actionable")
	assert.Contains(t, body, "crit-tp",
		"the criterion to run and mark is named — a hold nobody can act on is a stuck state")
}

// TestInterceptMutate_RollupHold_TestPlanCascadesWhenCriteriaEvaluated is the pair
// to the test above and differs from it in exactly one field, the criterion's
// status, so the two together show the hold discriminates on whether the criterion
// was evaluated rather than on the root being a test_plan.
func TestInterceptMutate_RollupHold_TestPlanCascadesWhenCriteriaEvaluated(t *testing.T) {
	fc := testPlanHoldFake(criterionPassStatus)
	handled, res := completeTestPlanOne(t, fc)
	require.True(t, handled, "the rollup arm claims a completed-status test_plan update")
	require.False(t, res.IsError, "the rollup should succeed: %s", toolResultText(res))
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.ElementsMatch(t, []string{"tp-1", "ts-crit", "ts-bare"}, fc.lastUpdate.GetSelection().GetIds(),
		"a test_step whose only criterion was run and passed is not held — the rule is not satisfiable by holding everything")
	assert.NotContains(t, toolResultText(res), heldNodesLiteral,
		"nothing was held, so the held bucket must not appear at all")
}

// criteriaOwningContainerTypes derives, from the project-domain BUILDERS rather
// than from any hand list, every node type that sits on a contains-path down to a
// criterion. Those are exactly the types a close must be able to cascade through
// and hold at: a type on that path which the rollup does not recognize as a
// container either strands the criterion below it (the type is a descendant) or
// closes silently without a cascade (the type is the named root).
//
// The builders are the right authority because they are the code that CREATES the
// contains edges. A census anchored on them goes red when a new criteria-bearing
// container type ships, which is the failure this whole file exists to prevent —
// anchoring it on the container predicate instead would be an identity check that
// can never fail.
//
// The project→ticket and ticket→plan links are FromID edges pointing at ids the
// caller supplies, so each builder's ROOT node is minted under the very id the next
// builder up was told to reference. That stitching is what joins the three subtrees
// into one contains skeleton, and it is asserted rather than assumed below.
func criteriaOwningContainerTypes(t *testing.T) map[kgtypes.NodeType]bool {
	t.Helper()

	const projSeed, tktSeed = "proj-seed", "tkt-seed"
	typeByID := map[string]kgtypes.NodeType{}
	var edges []kgwire.BatchEdge

	// A local id namespace per builder call keeps the node slices distinct; rootID,
	// when non-empty, mints the builder's index-0 node under the shared seed id.
	absorb := func(prefix, rootID string, nodes []*knowledgev1.Node, es []kgwire.BatchEdge) {
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = prefix + "-" + strconv.Itoa(i)
			if i == 0 && rootID != "" {
				ids[i] = rootID
			}
			typeByID[ids[i]] = kgtypes.NodeType(n.GetType())
		}
		resolve := func(idx int, id string) string {
			if idx >= 0 && idx < len(ids) {
				return ids[idx]
			}
			return id
		}
		for _, e := range es {
			edges = append(edges, kgwire.BatchEdge{
				FromID: resolve(e.FromIdx, e.FromID),
				ToID:   resolve(e.ToIdx, e.ToID),
				Type:   e.Type,
			})
		}
	}

	projNode, projEdges := projects.BuildProjectNode(
		projects.ProjectArgs{Name: "census project"}, "", backends.RemoteRef{}, backends.Group{})
	absorb("proj", projSeed, []*knowledgev1.Node{projNode}, projEdges)

	tktNodes, tktEdges := projects.BuildTicketNode(
		projects.TicketArgs{Name: "census ticket", ProjectID: projSeed},
		nil, nil, "", backends.RemoteRef{}, backends.Group{}, "")
	absorb("tkt", tktSeed, tktNodes, tktEdges)

	planNodes, planEdges, planErr := projects.BuildPlanGraph(projects.PlanArgs{
		Name:     "census plan",
		TicketID: tktSeed,
		Phases: []projects.PhaseArgs{{
			Name: "census phase",
			Steps: []projects.StepArgs{{
				Name:     "census step",
				Criteria: []projects.CriterionArgs{{Description: "census criterion", Type: "manual"}},
			}},
		}},
	}, nil, nil)
	require.NoError(t, planErr)
	absorb("plan", "", planNodes, planEdges)

	tpNodes, tpEdges := projects.BuildTestPlanGraph(projects.TestPlanArgs{
		Name: "census test plan",
		Steps: []projects.TestStepArgs{{
			Name:     "census test step",
			Criteria: []projects.CriterionArgs{{Description: "census test criterion", Type: "manual"}},
		}},
	})
	absorb("tp", "", tpNodes, tpEdges)

	// The ticket builder links project→ticket by FromID and the plan builder links
	// ticket→plan by FromID; the seeded ids above are what join the three subtrees
	// into one contains skeleton. Assert the join actually happened rather than
	// assuming it — a builder that stopped emitting the parent edge would silently
	// shrink the derived set on BOTH sides of the equality below.
	parents := map[string][]string{}
	joinedProject, joinedTicket := false, false
	for _, e := range edges {
		if e.Type != kgtypes.EdgeKGContains {
			continue
		}
		parents[e.ToID] = append(parents[e.ToID], e.FromID)
		switch e.FromID {
		case projSeed:
			joinedProject = true
		case tktSeed:
			joinedTicket = true
		}
	}
	require.True(t, joinedProject, "the ticket builder must still contains-link its project")
	require.True(t, joinedTicket, "the plan builder must still contains-link its ticket")

	// Walk UP from every criterion; every type on the way is a criteria-owning
	// container. The criterion itself is not — it is the evidence node the hold
	// protects, never a cascade target.
	owners := map[kgtypes.NodeType]bool{}
	seen := map[string]bool{}
	var climb func(id string)
	climb = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, p := range parents[id] {
			owners[typeByID[p]] = true
			climb(p)
		}
	}
	for id, nt := range typeByID {
		if nt == kgtypes.NodeCriterion {
			climb(id)
		}
	}
	return owners
}

// knowledgeNodeTypeUniverse is the set of knowledge node types the census sweeps
// the container predicate across. IT IS A UNIVERSE, NOT A CONTAINER LIST — that is
// what keeps the equality below from being an identity check. The predicate's own
// membership is never restated here, so a type wrongly ADDED to the predicate (a
// test_run, a finding) is caught by the sweep just as a type wrongly LEFT OUT is.
var knowledgeNodeTypeUniverse = []kgtypes.NodeType{
	kgtypes.NodeProject, kgtypes.NodeTicket, kgtypes.NodePlan, kgtypes.NodePhase,
	kgtypes.NodeStep, kgtypes.NodeCriterion, kgtypes.NodeDecision, kgtypes.NodeFinding,
	kgtypes.NodeResearch, kgtypes.NodeQuestion, kgtypes.NodeReference, kgtypes.NodeEvent,
	kgtypes.NodeDocument, kgtypes.NodeRule, kgtypes.NodeTestPlan, kgtypes.NodeTestStep,
	kgtypes.NodeTestRun, kgtypes.NodeAgent, kgtypes.NodeSkill, kgtypes.NodeThought,
	kgtypes.NodeCharge, kgtypes.NodeThoughtSession, kgtypes.NodePattern,
	kgtypes.NodeReuseCheck, kgtypes.NodeUseCase, kgtypes.NodeExample,
}

// TestInterceptMutate_RollupHold_ContainerSetCoversEveryCriteriaOwningContainer is
// the BOTH-DIRECTION census, and it is an exact set equality rather than a
// one-way containment.
//
//   - Builders → predicate: a criteria-owning container the rollup does not
//     recognize is the defect this change fixes. RED before it, because test_plan
//     and test_step are on a contains-path to a criterion and were not members.
//   - Predicate → builders: a member with no criteria-bearing contains-path is a
//     type the cascade would write status to for no structural reason.
//
// KNOWN POSITIVES, because a set equality between two sets that lost the same
// members is still an equality: the derived set is asserted non-empty, asserted to
// contain the two leaf owners by name, and asserted NOT to contain criterion or
// question — so a walk that collapsed to "everything" or to "nothing" fails here
// rather than passing quietly.
func TestInterceptMutate_RollupHold_ContainerSetCoversEveryCriteriaOwningContainer(t *testing.T) {
	owners := criteriaOwningContainerTypes(t)

	require.NotEmpty(t, owners,
		"the builders must still produce a criterion under a contains-path — an empty derivation proves nothing")
	assert.True(t, owners[kgtypes.NodeStep], "step owns criteria in the plan builder")
	assert.True(t, owners[kgtypes.NodeTestStep], "test_step owns criteria in the test-plan builder")
	assert.False(t, owners[kgtypes.NodeCriterion],
		"a criterion is the evidence node the hold protects, never a container on the path")
	assert.False(t, owners[kgtypes.NodeQuestion],
		"a question hangs off a plan and owns nothing — the walk must not sweep siblings in")

	// Direction 1 — every criteria-owning container the builders produce must be a
	// declared rollup container, including any the universe below does not know about.
	for nt := range owners {
		assert.True(t, isClientRollupContainer(nt),
			"%q sits on a builder contains-path down to a criterion, so a close must be able to cascade through it and hold at it", nt)
	}

	// Direction 2 — swept across the whole type universe, so the two sides agree
	// cell by cell rather than only where they already overlap. A declared container
	// with no criteria-bearing contains-path is a cascade target with no structural
	// warrant; a criteria-owning type the predicate rejects is the defect above.
	for _, nt := range knowledgeNodeTypeUniverse {
		assert.Equal(t, owners[nt], isClientRollupContainer(nt),
			"%q: the rollup container predicate and the builders' criteria-owning contains-paths must agree", nt)
	}
}
