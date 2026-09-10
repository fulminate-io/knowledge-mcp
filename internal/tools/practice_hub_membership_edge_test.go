// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_membership_edge_test.go drives the LAST two arms that could touch
// a membership: mutate(link) and mutate(unlink) naming the `sourced-from`
// relation itself.
//
// WHY THEY ARE A ROUTE AT ALL. Membership is one fact on two carriers — the
// `source_hub` metadata key and the node→hub `sourced-from` edge — and every
// other arm now writes them together or refuses. The edge arms were outside that
// rule because they take a relationship as an ordinary string: a link could
// repoint a member's edge to another hub while its key stayed, an unlink could
// remove the edge and leave the key, and a HUB-SCOPED link could draw a SECOND
// outgoing sourced-from edge to a pattern node, against the cardinality
// kgtypes declares. All three leave the state requirement 2 forbids, and the
// node invariant could not see them: a link plan carries an EdgeSpec, and the
// invariant inspected node bodies.
//
// SO THE RELATION IS NOT WRITABLE BY HAND ON PRACTICE. A membership is written by
// the create plan, which carries both carriers, and removed by the by-hub delete,
// which removes the member entirely. A caller reaching for the edge directly is
// asking for half of a write, and half a write is the split.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// hubEdgePayload renders one edge-arm call, optionally hub-scoped, over any
// relationship. It is spelled here rather than reused so a row can vary the
// RELATIONSHIP, which is the axis these rows are about.
func hubEdgePayload(operation, hub, from, to, relationship string) string {
	body := `{"operation":"` + operation + `","graph":"practice"`
	if hub != "" {
		body += `,"source_hub":"` + hub + `"`
	}
	return body + `,"from":"` + from + `","to":"` + to + `","relationship":"` + relationship + `"}`
}

// TestPracticeMembershipEdge_IsNeverWrittenByHand is the decided contract: on the
// practice family, neither edge arm writes or removes a `sourced-from` edge,
// whether or not the call names a hub.
func TestPracticeMembershipEdge_IsNeverWrittenByHand(t *testing.T) {
	for _, op := range []string{"link", "unlink"} {
		for _, scope := range []struct{ name, hub string }{
			{"unscoped", ""},
			{"hub-scoped", hubEndpointsHubA},
		} {
			t.Run(op+"/"+scope.name+"/member to another hub", func(t *testing.T) {
				// ROUTE d1 and d2: repoint or remove a member's own membership edge.
				fc, handled, res := driveHubTarget(t, hubEdgePayload(op, scope.hub,
					hubTargetMemberA, hubEndpointsHubB, string(kgtypes.EdgeSourcedFrom)))
				require.Truef(t, handled, "a %s naming the membership relation is claimed, not passed on", op)
				require.Truef(t, res.IsError, "%s must refuse: %s", op, toolResultText(res))

				body := toolResultText(res)
				assert.Contains(t, body, string(kgtypes.EdgeSourcedFrom),
					"the refusal names the relation the caller may not write")
				assert.Containsf(t, body, op, "and the arm it refused (%s)", op)
				assert.Empty(t, fc.execMutations, "nothing is written")
			})

			t.Run(op+"/"+scope.name+"/HUB to a member, the other direction", func(t *testing.T) {
				// THE `from` END IS AN AXIS, not a constant. A membership edge runs
				// node→hub, so a caller writing it backwards is a second half-write
				// shape, and a rule that only read the `to` end would admit it.
				fc, _, res := driveHubTarget(t, hubEdgePayload(op, scope.hub,
					hubEndpointsHubA, hubTargetMemberA, string(kgtypes.EdgeSourcedFrom)))
				require.Truef(t, res.IsError,
					"%s: the membership relation is refused whichever end names the hub: %s",
					op, toolResultText(res))
				assert.Empty(t, fc.execMutations)
			})

			t.Run(op+"/"+scope.name+"/member to a MEMBER", func(t *testing.T) {
				// ROUTE d1b, which this branch made reachable: the hub-scoped form
				// passes both endpoint checks when both are members of the named
				// hub, and then draws a SECOND outgoing sourced-from edge from one
				// member to another — a node grouped under a pattern.
				fc, _, res := driveHubTarget(t, hubEdgePayload(op, scope.hub,
					hubTargetMemberA, hubTargetMemberB, string(kgtypes.EdgeSourcedFrom)))
				require.Truef(t, res.IsError,
					"%s: a member may not be grouped under another member: %s", op, toolResultText(res))
				assert.Empty(t, fc.execMutations)
			})

			t.Run(op+"/"+scope.name+"/CONTROL: an ordinary relation still serves", func(t *testing.T) {
				// The same-run known positive. Without it every row above would be
				// satisfied by an arm that refused every edge write.
				_, _, res := driveHubTarget(t, hubEdgePayload(op, scope.hub,
					hubTargetMemberA, hubTargetMemberB, "relates-to"))
				assert.Falsef(t, res.IsError,
					"%s/%s: an ordinary practice relation is untouched by this rule: %s",
					op, scope.name, toolResultText(res))
			})
		}
	}

	t.Run("a CASE VARIANT is refused where the graph canonicalises it", func(t *testing.T) {
		// THE SECOND POSITION, driven end to end. The head gate compares EXACTLY,
		// because folding an edge type merges two stored families; a caller's
		// `SOURCED-FROM` therefore passes it untouched. The intra-practice link arm
		// then resolves the spelling against the graph's own vocabulary — which
		// holds `sourced-from` — and the rule is stated again there, on the
		// canonicalised value. Without that position the variant reaches the write
		// and lands the real membership edge.
		fc, handled, res := driveHubTarget(t, hubEdgePayload("link", "",
			hubTargetMemberA, hubEndpointsHubB, "SOURCED-FROM"))
		require.True(t, handled, "the canonicalised membership relation is claimed, not written")
		require.True(t, res.IsError, toolResultText(res))
		assert.Contains(t, toolResultText(res), string(kgtypes.EdgeSourcedFrom),
			"and the refusal names the CANONICAL spelling the graph resolved it to")
		assert.Empty(t, fc.execMutations, "nothing is written")
	})

	t.Run("CONTROL: the rule is the practice family's, not every family's", func(t *testing.T) {
		// A knowledge-graph link naming the same relation is not this rule's
		// business: `sourced-from` means a practice membership only where practice
		// hubs exist.
		_, _, res := driveHubTarget(t,
			`{"operation":"link","from":"`+hubTargetMemberA+`","to":"`+hubTargetMemberB+
				`","relationship":"`+string(kgtypes.EdgeSourcedFrom)+`"}`)
		assert.NotContains(t, toolResultText(res), "written by hand",
			"the practice membership rule must not reach another family's link")
	})
}

// TestPracticeHubNesting_AHubIsNeverItselfGrouped is the other cardinality the
// review found unstated: a `source` node that is ITSELF grouped under a hub.
//
// WHY IT IS REFUSED BOTH WAYS. A hub is the identity of a collection, and a
// collection that is a member of another collection makes "the hub a node is
// grouped under" a chain rather than a value — every reader of the key, the
// browse and the by-hub delete among them, resolves one hop and would silently
// disagree with a traverse that walks the whole chain. Nothing in the surface
// expresses a nested collection, so the honest answer is to refuse both the
// create that would nest one and the use of a nested node as a hub.
func TestPracticeHubNesting_AHubIsNeverItselfGrouped(t *testing.T) {
	t.Run("a create of a `source` node UNDER a hub is refused naming both", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t,
			`{"operation":"create","graph":"practice","source_hub":"`+hubEndpointsHubA+
				`","type":"`+string(kgtypes.NodeSource)+`","name":"Nested","summary":"a hub under a hub"}`)
		require.True(t, handled)
		require.True(t, res.IsError, toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, hubEndpointsHubA, "the refusal names the hub the create asked for")
		assert.Contains(t, body, string(kgtypes.NodeSource), "and the type that may not be grouped")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("a NESTED hub may not be used as a hub", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t,
			`{"operation":"create","graph":"practice","source_hub":"`+hubNestedHub+
				`","type":"pattern","name":"n","summary":"s"}`)
		require.True(t, handled)
		require.True(t, res.IsError, toolResultText(res))
		assert.Contains(t, toolResultText(res), hubNestedHub, "the refusal names the nested hub")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("CONTROL: an ordinary create under an ordinary hub still serves", func(t *testing.T) {
		_, _, res := driveHubTarget(t, hubResolvePayload("create", hubEndpointsHubA))
		assert.False(t, res.IsError, toolResultText(res))
	})
}

// TestPracticeHubDelete_SweepsTheHubThroughItsOwnKey is the by-hub delete's
// decided shape, and it is ONE predicate, not two writes.
//
// A HUB CARRIES ITS OWN ID UNDER THE HUB KEY. The recipe landing and the
// migration driver both write it on purpose, and this is what it buys: the
// selection "every practice node whose source_hub equals H" already contains H,
// so the hub is swept with its members atomically, in the write the owner's
// ruling asks for. An earlier shape here added a SECOND by-id write to remove the
// hub; unguarded on the standalone delete tool it destroyed whatever id it was
// handed, and it made the pair non-atomic for no gain.
//
// THE PLAN IS THE SUBJECT, not the fake's answer: the row reads the Selection the
// call compiled and asserts what it selects BY, because a count that comes from a
// canned response would hold whatever the plan carried.
func TestPracticeHubDelete_SweepsTheHubThroughItsOwnKey(t *testing.T) {
	fc, _, res := driveHubTarget(t, hubResolvePayload("delete", hubEndpointsHubA))
	require.False(t, res.IsError, toolResultText(res))
	require.Len(t, fc.execMutations, 1, "the by-hub delete is ONE write")

	sel := fc.execMutations[0].GetSelection()
	require.Len(t, sel.GetMetadataPredicates(), 1, "and it selects by exactly one predicate")
	assert.Equal(t, kgtypes.MetaKeySourceHub, sel.GetMetadataPredicates()[0].GetKey())
	assert.Equal(t, hubEndpointsHubA, sel.GetMetadataPredicates()[0].GetValue())
	assert.Empty(t, sel.GetIds(),
		"and carries NO ids list: `ids` short-circuits the predicate arms, so a plan carrying both would "+
			"delete the ids alone and leave every member behind")
}

// TestPracticeHubDelete_PreviewResolvesTheSameSet is the dry run's own row, and
// the reason it exists is that its predecessor could not fail: the preview's
// exec fake returned a canned two-node answer whatever plan it was given, so the
// rendered count came from the fake rather than from the selection.
//
// THIS ROW READS THE PLAN. The preview and the real delete resolve their set
// through one function, so the assertion is that the preview's QueryPlan carries
// the same predicate and, like the write, no short-circuiting ids list.
func TestPracticeHubDelete_PreviewResolvesTheSameSet(t *testing.T) {
	fc := practiceHubTargetFake(t)
	// THE STANDALONE `delete` TOOL owns the dry run, and its hub axis is spelled
	// `source`. Driving the mutate arm here would exercise a shape that carries no
	// dry_run at all and would assert nothing about the preview.
	_, previewErr := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "delete", json.RawMessage(
		`{"graph":"practice","source":"`+hubEndpointsHubA+`","dry_run":true}`))
	require.NoError(t, previewErr)

	var previews []*knowledgev1.QueryPlan
	for _, req := range fc.execRequests {
		if q, isQuery := req.GetPlan().(*knowledgev1.ExecuteRequest_Query); isQuery {
			if len(q.Query.GetSelection().GetMetadataPredicates()) > 0 {
				previews = append(previews, q.Query)
			}
		}
	}
	require.Len(t, previews, 1, "the preview resolves its set with ONE predicate read")
	assert.Equal(t, kgtypes.MetaKeySourceHub, previews[0].GetSelection().GetMetadataPredicates()[0].GetKey())
	assert.Empty(t, previews[0].GetIds(),
		"and carries no ids list: `ids` short-circuits the predicate on the READ path too, so a preview "+
			"naming the hub by id would list the hub and none of its members")
	assert.Empty(t, fc.execMutations, "a dry run writes nothing")
}
