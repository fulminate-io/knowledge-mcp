// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// practice_hub_recording_test.go — the three things a hub actually IS on the
// wire: the by-hub delete selection, the metadata stamp on a created node, and
// the node→hub edge that rides the same batch.
//
// EACH OF THE THREE WAS BUILT WITH NO OBSERVER. Disabling the by-hub arm in
// compileDelete, dropping the stamp in withSourceHub, inverting the edge's
// direction and deleting the edge emission outright each left the whole client
// suite green, so requirement 2's key, edge direction and edge cardinality and
// requirement 3b's whole mechanism rested on the code being read correctly
// rather than on anything failing when it was not. The assertions below are
// written against the COMPILED REQUEST rather than against a rendered string,
// because the compiled request is the artifact the server acts on and a renderer
// can agree with a wrong plan.

// compiledDeletePlan compiles a delete payload and returns its MutationPlan.
func compiledDeletePlan(t *testing.T, payload string) *knowledgev1.MutationPlan {
	t.Helper()
	req, ok := compileDelete(json.RawMessage(payload))
	require.True(t, ok, "the payload must compile: %s", payload)
	m, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation)
	require.True(t, isMutation, "a delete compiles to a MutationPlan")
	return m.Mutation
}

// TestCompileDelete_ByHubSelectsOnTheHubKey is requirement 3b's compile half.
func TestCompileDelete_ByHubSelectsOnTheHubKey(t *testing.T) {
	plan := compiledDeletePlan(t, `{"graph":"practice","source":"hub-1"}`)

	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, plan.GetKind())
	assert.Empty(t, plan.GetSelection().GetIds(),
		"a by-hub delete selects by predicate, never by an id list it did not receive")

	preds := plan.GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1,
		"the hub rides exactly ONE metadata predicate — the server resolves a delete's predicates on both flavors")
	assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey(),
		"and on the hub key, never on `source`, which practice nodes already use for their own provenance")
	assert.Equal(t, "hub-1", preds[0].GetValue())
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, preds[0].GetOp())

	// THE NEGATIVE CONTROL: without a hub there is no hub predicate, so the
	// assertions above are the selector being READ rather than a predicate
	// stamped unconditionally.
	byIDs := compiledDeletePlan(t, `{"graph":"practice","ids":["p1"]}`)
	assert.Empty(t, byIDs.GetSelection().GetMetadataPredicates(),
		"a by-ids delete carries no hub predicate")
	assert.Equal(t, []string{"p1"}, byIDs.GetSelection().GetIds())

	// AND THE TWO-AXIS REFUSAL, because this is a destructive op: ids AND a hub
	// both select, and picking either is a guess about what to destroy.
	_, ok := compileDelete(json.RawMessage(`{"graph":"practice","source":"hub-1","ids":["p1"]}`))
	assert.False(t, ok, "two selection axes on a delete are denied, never resolved")
}

// TestDeleteDryRun_ByHubPreviewsExactlyTheHubsMembers is requirement 3b's SAFETY
// half, and it is the one that found a defect rather than pinning a mechanism.
//
// The preview is the only way a caller sees what a by-hub delete would remove
// before it removes it, and the preview planner had no by-hub arm at all: a
// dry-run carrying `source` fell past the by-ids branch into the prune-by-age
// shape, failed to build a selection, and answered "dry_run requires either
// ids[] or a valid older_than". So the destructive op shipped with its preview
// unreachable, which is the one arm of a delete a caller is told to trust.
func TestDeleteDryRun_ByHubPreviewsExactlyTheHubsMembers(t *testing.T) {
	var seen *knowledgev1.QueryPlan
	members := []*knowledgev1.Node{
		{Id: "p1", Type: "pattern", SymbolName: "member one"},
		{Id: "p2", Type: "pattern", SymbolName: "member two"},
	}
	exec := func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		q, ok := req.GetPlan().(*knowledgev1.ExecuteRequest_Query)
		require.True(t, ok, "a preview READS; it must never compile to a mutation")
		seen = q.Query
		return &knowledgev1.ExecuteResponse{Nodes: members}, nil
	}

	out, handled := dispatchDeletePreview(context.Background(), exec,
		json.RawMessage(`{"graph":"practice","source":"hub-1","dry_run":true}`))
	require.True(t, handled, "the preview seam claims every dry-run delete shape")
	require.False(t, out.IsError, "a by-hub dry-run is a supported shape: %s", out.Content[0].Text)

	require.NotNil(t, seen, "the preview must have issued its read")
	preds := seen.GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1,
		"the preview resolves the IDENTICAL node set the real delete would: the same one hub predicate")
	assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey())
	assert.Equal(t, "hub-1", preds[0].GetValue())

	body := out.Content[0].Text
	assert.Contains(t, body, "p1")
	assert.Contains(t, body, "p2")
	assert.Contains(t, body, "would delete 2 node(s)",
		"the preview counts exactly the hub's members, and its verb says nothing was deleted — a dry-run that reads as a completed delete is the footgun this render exists to avoid")
}

// TestCompileMutateCreate_RecordsTheHubBothWays is requirement 2(a) and 2(b) on
// the write path: the metadata key AND the node→hub edge, in one batch.
func TestCompileMutateCreate_RecordsTheHubBothWays(t *testing.T) {
	bodiesOf := func(t *testing.T, payload string) ([]*knowledgev1.NodeBody, []*knowledgev1.BatchEdgeSpec) {
		t.Helper()
		var a mutateArgs
		require.NoError(t, json.Unmarshal([]byte(payload), &a))
		bodies, edges, ok := createPayload(a)
		require.True(t, ok, "the create payload must compile")
		return bodies, edges
	}

	t.Run("the hub id is stamped on the created node", func(t *testing.T) {
		bodies, _ := bodiesOf(t, `{"operation":"create","graph":"practice","source_hub":"hub-1","type":"pattern","name":"P","summary":"s","source":"recipe:eip"}`)
		require.Len(t, bodies, 1)
		assert.Equal(t, "hub-1", bodies[0].GetMetadata()[kgtypes.MetaKeySourceHub],
			"a hub-scoped browse, a by-hub delete and the ranked search's member resolution all predicate on this key")
		assert.Equal(t, "recipe:eip", bodies[0].GetSource(),
			"and the node's own top-level source field is left exactly as the caller sent it")
	})

	t.Run("one node-to-hub edge per created body, in that direction", func(t *testing.T) {
		bodies, edges := bodiesOf(t, `{"operation":"create_batch","graph":"practice","source_hub":"hub-1","nodes":[{"type":"pattern","name":"A","summary":"s"},{"type":"pattern","name":"B","summary":"s"}]}`)
		require.Len(t, bodies, 2)
		require.Len(t, edges, 2,
			"exactly one sourced-from per created node: a node belongs to one hub, and a second edge is a failure rather than a merge")

		for i, e := range edges {
			assert.Equal(t, string(kgtypes.EdgeSourcedFrom), e.GetType())
			// THE DIRECTION, asserted on BOTH endpoints. Checking only that the
			// hub appears somewhere on the edge passes under an inversion, which
			// is exactly the mutation this row exists to kill.
			assert.Equal(t, int32(i), e.GetFromIdx(),
				"the edge runs FROM the created node: the cardinality requirement 2(b) states lives at the node, as its one outgoing edge")
			assert.Equal(t, "hub-1", e.GetToId(),
				"and TO the hub, so a walk out of the node reaches its hub and a walk in to the hub enumerates its members")
			assert.Empty(t, e.GetFromId(), "the source endpoint is the batch slot, not a pre-existing id")
			assert.Equal(t, int32(-1), e.GetToIdx(), "and the hub is an existing node, addressed by id")
		}
	})

	// THE PRECEDENCE THIS COMPILER OWNS, and the reason it needs its own row.
	// withSourceHub leaves a body's explicit `source_hub` key alone rather than
	// overwriting it with the call's hub. The tools layer now refuses a body whose
	// key DISAGREES with the call (guardPracticeHubBodyHubs), so no caller-facing
	// drive can reach this compiler with a disagreeing pair any more — which is
	// exactly why the rule is asserted HERE, at the layer that defines it, on the
	// compiler's own seam. Without this row the precedence branch is a decision
	// nothing observes: inverted to an unconditional overwrite, every other test
	// in both packages stays green.
	t.Run("an explicit body key is left alone, not overwritten by the call's hub", func(t *testing.T) {
		bodies, edges := bodiesOf(t, `{"operation":"create_batch","graph":"practice","source_hub":"hub-1","nodes":[{"type":"pattern","name":"A","summary":"s","metadata":{"source_hub":"hub-2","other":"kept"}}]}`)
		require.Len(t, bodies, 1)
		assert.Equal(t, "hub-2", bodies[0].GetMetadata()[kgtypes.MetaKeySourceHub],
			"the body's own key wins: this compiler stamps, it does not overwrite")
		assert.Equal(t, "kept", bodies[0].GetMetadata()["other"],
			"and every other key the body carried survives the stamp")
		require.Len(t, edges, 1)
		assert.Equal(t, "hub-1", edges[0].GetToId(),
			"the EDGE still follows the call's hub, which is the disagreement the tools gate exists to refuse")
	})

	// THE NEGATIVE CONTROL for both halves: no hub, no key and no edge. Without
	// it a compiler that stamped and linked unconditionally satisfies every
	// assertion above.
	t.Run("no hub adds no key and no edge", func(t *testing.T) {
		bodies, edges := bodiesOf(t, `{"operation":"create","graph":"practice","type":"pattern","name":"P","summary":"s"}`)
		require.Len(t, bodies, 1)
		assert.NotContains(t, bodies[0].GetMetadata(), kgtypes.MetaKeySourceHub)
		assert.Empty(t, edges, "an unhubbed practice node carries no sourced-from edge")
	})
}

// TestCompileDelete_ByHubAcceptsTheMutateArmSpelling is requirement 6's
// SECOND-SPELLING leg, and it exists because one compiler serves two tools whose
// schemas publish different names for the same selector.
//
// THE DELETE TOOL PUBLISHES `source`; mutate publishes `source_hub`, because
// `source` is already the node's own provenance field on every mutate arm. Both
// route through compileDelete, so a mutate(delete) carrying the only spelling its
// own schema declares used to fall past the by-hub branch entirely, land in the
// prune-by-age shape, fail its selection and deny the compile — a by-hub delete
// that is unreachable from half the surface that documents it.
func TestCompileDelete_ByHubAcceptsTheMutateArmSpelling(t *testing.T) {
	plan := compiledDeletePlan(t, `{"operation":"delete","graph":"practice","source_hub":"hub-1"}`)

	preds := plan.GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1, "the mutate spelling selects the same one hub predicate the delete tool's does")
	assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey())
	assert.Equal(t, "hub-1", preds[0].GetValue())

	// THE TWO SPELLINGS ARE ONE AXIS, NOT TWO. Sending both with the SAME value is
	// a caller repeating itself and compiles; sending them with DIFFERENT values
	// asks two questions at once about what to destroy, and picking either is a
	// guess.
	agree := compiledDeletePlan(t, `{"graph":"practice","source":"hub-1","source_hub":"hub-1"}`)
	require.Len(t, agree.GetSelection().GetMetadataPredicates(), 1,
		"one axis, so one predicate however many times the caller spells it")

	_, ok := compileDelete(json.RawMessage(`{"graph":"practice","source":"hub-1","source_hub":"hub-2"}`))
	assert.False(t, ok, "two DIFFERENT hubs on one destructive call are denied, never resolved")

	// AND THE HUB STILL EXCLUDES IDS under the mutate spelling too, or the
	// two-axis refusal would hold for one spelling and not the other.
	_, idsOK := compileDelete(json.RawMessage(`{"operation":"delete","graph":"practice","source_hub":"hub-1","ids":["p1"]}`))
	assert.False(t, idsOK, "ids and a hub both select, whichever spelling the hub arrived under")
}

// TestDeleteDryRun_ByHubPreviewsUnderTheMutateSpelling keeps the preview and the
// real delete resolving the identical set under BOTH spellings. A preview arm
// that reads only one of them leaves the other's dry-run answering that it needs
// ids — a destructive op whose preview is unreachable from the surface that
// documents it, which is the defect the by-hub preview arm was added to fix.
func TestDeleteDryRun_ByHubPreviewsUnderTheMutateSpelling(t *testing.T) {
	var seen *knowledgev1.QueryPlan
	exec := func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		q, ok := req.GetPlan().(*knowledgev1.ExecuteRequest_Query)
		require.True(t, ok, "a preview READS")
		seen = q.Query
		return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{{Id: "p1", Type: "pattern"}}}, nil
	}

	out, handled := dispatchDeletePreview(context.Background(), exec,
		json.RawMessage(`{"operation":"delete","graph":"practice","source_hub":"hub-1","dry_run":true}`))
	require.True(t, handled)
	require.False(t, out.IsError, "a by-hub dry-run under the mutate spelling is a supported shape: %s", out.Content[0].Text)

	require.NotNil(t, seen)
	preds := seen.GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1)
	assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey())
	assert.Equal(t, "hub-1", preds[0].GetValue())
}
