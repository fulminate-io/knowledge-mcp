// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_invariant_test.go holds the ONE assertion every hub row now ends
// with, and the node-level invariant it states.
//
// WHY AN INVARIANT RATHER THAN MORE ROWS. Requirement 2 makes membership one fact
// on two carriers: the `source_hub` metadata key and the node→hub `sourced-from`
// edge. Five review rounds each found a different route to a state where those
// two disagree — a body naming another hub, an upsert dropping the key, a hub
// that names no node, a body key with no edge — and each was closed by a row
// written for that route. A sixth route is always available, because the rows
// assert the ROUTE and nothing asserts the STATE.
//
// SO THIS ASSERTS THE STATE. It runs over the plan a call would apply, against
// the pre-state the fake seeds, and reports whether that plan could leave a node
// whose two carriers disagree. Every hub drive in this package ends with it, so a
// new route fails here rather than after the next review.
//
// IT IS TWO CLAUSES, because a plan writes the carriers two different ways. A
// CREATE plan carries both, so its own bodies and edges must agree with each
// other. Every other plan carries only the metadata — the server refuses edges on
// an UPSERT plan and an UPDATE has none — so the key it writes must agree with
// the hub the node is ALREADY edged to, which is the hub it is stored under.
//
// IT COUNTS WHAT IT CHECKED, and the counting is not bookkeeping: an assertion
// that silently checks nothing is the vacuous pass this file exists to prevent,
// so TestPracticeHubInvariant_IsNotVacuous holds the count to a positive number
// on the shapes that write a hub.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// seededPracticeHubOf reads the hub a fake has the node stored under, which is
// the pre-state the non-create clause compares against. An unseeded id is not a
// member, so it reports "".
func seededPracticeHubOf(fc *fakeGraphCaller, id string) string {
	seeded, ok := fc.queryResponsesByGraphName[graphKey{Type: "practice", Name: workingset.DefaultInstanceName}][id]
	if !ok {
		return ""
	}
	node, decoded := decodeSeededNode(seeded)
	if !decoded {
		return ""
	}
	return node.GetMetadata()[kgtypes.MetaKeySourceHub]
}

// planSourceHubEdge returns the hub the plan links body index i to, or "" when it
// emits no `sourced-from` edge for that body.
func planSourceHubEdge(plan *knowledgev1.MutationPlan, i int) string {
	for _, e := range plan.GetEdges() {
		if e.GetType() == string(kgtypes.EdgeSourcedFrom) && int(e.GetFromIdx()) == i {
			return e.GetToId()
		}
	}
	return ""
}

// assertHubCarriersCannotSplit is the invariant. It returns the number of
// carriers it checked so a caller can refuse a vacuous pass.
func assertHubCarriersCannotSplit(t *testing.T, fc *fakeGraphCaller, plan *knowledgev1.MutationPlan) int {
	t.Helper()
	checked := 0
	create := plan.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE
	for i, body := range plan.GetNodeBodies() {
		key := body.GetMetadata()[kgtypes.MetaKeySourceHub]
		if body.GetType() == string(kgtypes.NodeSource) {
			// A HUB IS ITS OWN SHAPE. It carries its OWN id under the hub key —
			// which is what lets one predicate sweep the hub with its members — and
			// it draws NO membership edge, because it is grouped under nothing but
			// itself. A hub keyed elsewhere, or edged to another node, is the chain
			// this invariant exists to forbid.
			checked++
			// ONE ASSERTION, AND IT REQUIRES BOTH ENDS NON-EMPTY. That is the T3
			// this clause shipped with: an equality test ALONE is satisfied when a
			// hub body carries neither an id nor a key, which is exactly the plan a
			// tool-created hub used to compile — so the clause passed on the one
			// shape the self-key regression produced. Split into three statements
			// the two emptiness halves cannot be killed apart, because equality
			// holds for the empty pair and catches whichever half survives; stated
			// as one condition there is one guard and one kill.
			assert.Truef(t, body.GetId() != "" && body.GetId() == key,
				"body %d is a %q node with id %q and %s %q: a hub carries its OWN id there, and both ends "+
					"must be present — an absent key is a hub the by-hub delete's predicate cannot reach",
				i, kgtypes.NodeSource, body.GetId(), kgtypes.MetaKeySourceHub, key)
			assert.Emptyf(t, planSourceHubEdge(plan, i),
				"body %d is a hub and draws a %s edge: a hub is grouped under nothing but itself",
				i, kgtypes.EdgeSourcedFrom)
			continue
		}
		if create {
			checked++
			assert.Equalf(t, key, planSourceHubEdge(plan, i),
				"body %d writes %s=%q and a %s edge to a different hub — the two carriers of one membership "+
					"must agree, or the node is filed under a hub nothing links it to",
				i, kgtypes.MetaKeySourceHub, key, kgtypes.EdgeSourcedFrom)
			continue
		}
		// A non-create plan carries NO edge, so whatever it does to the key has to
		// leave the key agreeing with the edge the node already has. THE COUNTER
		// MOVED BELOW THIS TEST rather than above it: counting a body the clauses
		// below do not assert reports a carrier checked when none was, which is
		// exactly the vacuous pass the count exists to catch.
		stored := seededPracticeHubOf(fc, body.GetId())
		checked++
		if key == "" && plan.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			// A MERGE plan that does not mention the key leaves it alone, so there
			// is nothing here to disagree with the edge. Only the REPLACE arm can
			// drop a key by omitting it.
			continue
		}
		if key == "" {
			// THE DROP-THE-KEY ROUTE, which this clause exists for. A body that
			// omits the key REPLACES the stored metadata with one that has none —
			// upsert is the arm with those semantics — while the node's
			// `sourced-from` edge survives untouched. A member edited that way is
			// filed under no hub by its key and under its old hub by its edge.
			assert.Emptyf(t, stored,
				"body %d omits %s on a plan that carries no %s edge, so a member's key would be dropped "+
					"while its edge survives", i, kgtypes.MetaKeySourceHub, kgtypes.EdgeSourcedFrom)
			continue
		}
		assert.Equalf(t, stored, key,
			"body %d rewrites %s to %q on a plan that carries no %s edge, so the node's edge would stay on "+
				"the hub it is stored under and the two carriers would disagree",
			i, kgtypes.MetaKeySourceHub, key, kgtypes.EdgeSourcedFrom)
	}
	// THE EDGE ARM. A link or unlink plan carries no body at all — its write is an
	// EdgeSpec over a selection — so the clauses above see nothing, and the two
	// arms that can repoint or remove a membership edge were outside the
	// invariant entirely. A plan naming the membership relation is a split by
	// construction: it moves one carrier and cannot touch the other.
	if spec := plan.GetEdgeSpec(); spec != nil && spec.GetRelationship() != "" {
		checked++
		assert.NotEqualf(t, string(kgtypes.EdgeSourcedFrom), spec.GetRelationship(),
			"the plan writes a %s edge from %v to %q with no metadata, so it moves one membership carrier "+
				"and leaves the other where it was",
			kgtypes.EdgeSourcedFrom, plan.GetSelection().GetIds(), spec.GetToId())
	}
	// The UPDATE shape writes its metadata through set_metadata over a selection
	// rather than through bodies, and it carries no edge either.
	if key, present := plan.GetSetMetadata()[kgtypes.MetaKeySourceHub]; present {
		for _, id := range plan.GetSelection().GetIds() {
			checked++
			assert.Equalf(t, seededPracticeHubOf(fc, id), key,
				"the update writes %s=%q onto %q on a plan that carries no %s edge",
				kgtypes.MetaKeySourceHub, key, id, kgtypes.EdgeSourcedFrom)
		}
	}
	return checked
}

// TestPracticeHubInvariant_IsNotVacuous is the no-skip guard for the assertion
// above, and the reason it returns a count.
//
// EVERY SHAPE BELOW WRITES A HUB, so the invariant must have something to check
// on each of them. A helper that checked nothing would satisfy every hub row in
// this package silently, which is exactly the failure mode the rows it guards
// were written to end.
func TestPracticeHubInvariant_IsNotVacuous(t *testing.T) {
	for _, row := range []struct{ name, payload string }{
		{"create", hubResolvePayload("create", hubEndpointsHubA)},
		{"create_batch", hubResolvePayload("create_batch", hubEndpointsHubA)},
		{"upsert", hubTargetPayload("upsert", hubEndpointsHubA, hubTargetsFor("upsert", hubTargetMemberA))},
		{"hub-less upsert of a member", hubCarryPayload(hubTargetMemberA, "")},
	} {
		t.Run(row.name, func(t *testing.T) {
			fc, _, res := driveHubTarget(t, row.payload)
			require.False(t, res.IsError, toolResultText(res))
			checked := assertHubCarriersCannotSplit(t, fc, hubTargetWrittenPlan(t, fc, row.payload))
			assert.Positivef(t, checked,
				"%s writes a hub, so the invariant must have a carrier to check — a zero here is the "+
					"vacuous pass the count exists to catch", row.name)
		})
	}
}

// TestPracticeHubInvariant_CatchesEachSplitRoute drives the assertion itself
// against hand-built plans, one per route the reviews found, so the helper is
// known to FAIL on a split rather than merely to pass on the tree.
//
// IT IS THE HELPER'S OWN KNOWN POSITIVE. Every row in this package ends with this
// assertion; if it could not fail, all of them would be decoration.
func TestPracticeHubInvariant_CatchesEachSplitRoute(t *testing.T) {
	fc := practiceHubTargetFake(t)
	for _, row := range []struct {
		name string
		plan *knowledgev1.MutationPlan
	}{
		{"a create writing the key with NO edge", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: "split-1", Type: "pattern",
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubA},
			}},
		}},
		{"a create whose edge names a DIFFERENT hub", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: "split-2", Type: "pattern",
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubA},
			}},
			Edges: []*knowledgev1.BatchEdgeSpec{{
				FromIdx: 0, ToIdx: -1, ToId: hubEndpointsHubB, Type: string(kgtypes.EdgeSourcedFrom),
			}},
		}},
		{"an upsert repointing a member's key", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: hubTargetMemberA, Type: "pattern",
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubB},
			}},
		}},
		{"an upsert whose body DROPS a member's key", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: hubTargetMemberA, Type: "pattern", Metadata: map[string]string{},
			}},
		}},
		{"a link writing the membership edge by hand", &knowledgev1.MutationPlan{
			Kind:      knowledgev1.MutationPlan_MUTATION_KIND_LINK,
			Selection: &knowledgev1.Selection{Ids: []string{hubTargetMemberA}},
			EdgeSpec: &knowledgev1.EdgeSpec{
				Relationship: string(kgtypes.EdgeSourcedFrom), ToId: hubEndpointsHubB, Forward: true,
			},
		}},
		// THE EMPTY PAIR, which is the shape a tool-created hub compiled to and
		// the one an equality test alone admits.
		{"a hub carrying NEITHER an id nor a key", &knowledgev1.MutationPlan{
			Kind:       knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{Type: string(kgtypes.NodeSource)}},
		}},
		{"a hub carrying a key but NO id", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Type:     string(kgtypes.NodeSource),
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: "hub-z"},
			}},
		}},
		{"a hub keyed to ANOTHER node", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: "hub-x", Type: string(kgtypes.NodeSource),
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubA},
			}},
		}},
		// THE EDGE POINTS AT THE HUB ITSELF, which is the one shape only the hub
		// clause can object to: the create clause compares the key with the edge
		// and finds them equal, so a row whose edge named another node would be
		// caught by that clause instead and would prove nothing about this one.
		{"a hub drawing a membership edge of its own", &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: "hub-y", Type: string(kgtypes.NodeSource),
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: "hub-y"},
			}},
			Edges: []*knowledgev1.BatchEdgeSpec{{
				FromIdx: 0, ToIdx: -1, ToId: "hub-y", Type: string(kgtypes.EdgeSourcedFrom),
			}},
		}},
		{"an update setting the key on a member of another hub", &knowledgev1.MutationPlan{
			Kind:        knowledgev1.MutationPlan_MUTATION_KIND_UPDATE,
			Selection:   &knowledgev1.Selection{Ids: []string{hubTargetMemberA}},
			SetMetadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubB},
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			split := &testing.T{}
			checked := assertHubCarriersCannotSplit(split, fc, row.plan)
			assert.Positive(t, checked, "the route must be checked, not skipped")
			assert.True(t, split.Failed(), "the invariant must FAIL on a plan that splits the two carriers")
		})
	}

	t.Run("the control: a well-formed create passes", func(t *testing.T) {
		clean := &testing.T{}
		checked := assertHubCarriersCannotSplit(clean, fc, &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
			NodeBodies: []*knowledgev1.NodeBody{{
				Id: "clean-1", Type: "pattern",
				Metadata: map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubA},
			}},
			Edges: []*knowledgev1.BatchEdgeSpec{{
				FromIdx: 0, ToIdx: -1, ToId: hubEndpointsHubA, Type: string(kgtypes.EdgeSourcedFrom),
			}},
		})
		assert.Positive(t, checked)
		assert.False(t, clean.Failed(), "a plan whose carriers agree must pass")
	})
}

// assertHubDriveCannotSplit runs the invariant on whatever plan ONE drive would
// apply, and is the call every hub drive helper in this package ends with.
//
// IT SKIPS A REFUSED CALL, for observeHubDisposition's reason: a refusal is never
// passed on, so compiling its payload would assert against a plan nothing will
// ever apply. It skips a payload that does not compile for the same reason —
// there is no write to check.
func assertHubDriveCannotSplit(t *testing.T, fc *fakeGraphCaller, payload string, res kgtools.ToolResult) {
	t.Helper()
	if res.IsError {
		return
	}
	// THE INVARIANT IS THE PRACTICE FAMILY'S. `sourced-from` means a practice
	// membership only where practice hubs exist, so a knowledge or code write
	// naming the same relation is not this rule's business — asserting over it
	// would refuse a shape no requirement forbids.
	var routed struct {
		Graph string `json:"graph"`
	}
	if err := json.Unmarshal([]byte(payload), &routed); err != nil || routed.Graph != string(kgtypes.GraphPractice) {
		return
	}
	if len(fc.execMutations) > 0 {
		assertHubCarriersCannotSplit(t, fc, fc.execMutations[len(fc.execMutations)-1])
		return
	}
	req, ok := engine.Compile("mutate", json.RawMessage(payload))
	if !ok {
		return
	}
	if m, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
		assertHubCarriersCannotSplit(t, fc, m.Mutation)
	}
}
