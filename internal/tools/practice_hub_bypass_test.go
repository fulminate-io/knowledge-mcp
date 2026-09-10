// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_bypass_test.go drives the two callers that never reach
// InterceptMutate, which is why the hub rules moved into the engine.
//
// THE ROUTE TABLE, derived rather than remembered. Every hub rule this branch
// built lived in the mutate intercept. Two callers reach the engine without
// passing it: the recipe landing sends its create_batch through executeMutate,
// the tools' own compile-and-execute funnel (sixteen call sites share it), and the
// standalone `delete` tool reaches engine.Dispatch. Both therefore ran no hub
// rule at all — the landing wrote under whatever hub it was told, and the delete
// tool's `source` deleted whatever id it was handed, which in the audit's live
// run tombstoned a pattern node.
//
// SO THESE ROWS DRIVE THE BYPASS, NOT THE INTERCEPT. Each one goes through the
// path the caller actually takes, and each is red when the engine-layer rule is
// deleted, which is what says the POSITION carries them; the rules that also have
// a tools-layer position red rows on both sides.

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// landingShapedBatch renders the create_batch the recipe landing composes: a hub
// body carrying its OWN id under the hub key, a member carrying the hub's id, and
// the member's own `sourced-from` edge by slot. hub=="" omits the hub body, which
// is the reuse run.
func landingShapedBatch(hubID, memberHub string, withHubBody bool) string {
	nodes := ""
	edgeFrom := 0
	if withHubBody {
		nodes = `{"type":"` + string(kgtypes.NodeSource) + `","id":"` + hubID +
			`","name":"h","summary":"s","metadata":{"` + kgtypes.MetaKeySourceHub + `":"` + hubID + `"}},`
		edgeFrom = 1
	}
	nodes += `{"type":"pattern","id":"landed-1","name":"m","summary":"s","metadata":{"` +
		kgtypes.MetaKeySourceHub + `":"` + memberHub + `"}}`
	return `{"operation":"create_batch","graph":"practice","nodes":[` + nodes +
		`],"edges":[{"from_idx":` + strconv.Itoa(edgeFrom) + `,"to_idx":-1,"to_id":"` + memberHub +
		`","type":"` + string(kgtypes.EdgeSourcedFrom) + `"}]}`
}

// TestPracticeHubBypass_LandingPathRunsTheRules drives the executeMutate funnel,
// which is the route the recipe landing takes.
func TestPracticeHubBypass_LandingPathRunsTheRules(t *testing.T) {
	t.Run("a batch that CREATES its own hub is admitted from the payload", func(t *testing.T) {
		// THE KNOWN POSITIVE, and the shape the landing actually writes: a hub
		// keyed to its own id, its member, and the member's edge, all in one
		// batch. The hub id resolves to nothing yet — the batch is what makes it
		// exist — so it is admitted from the payload rather than read.
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(landingShapedBatch("new-hub-1", "new-hub-1", true)))
		require.NoError(t, err, "the landing's own shape must land")
		assert.Len(t, fc.execMutations, 1, "and it is ONE create_batch")
	})

	t.Run("a REUSE batch under a hub that is not a hub is refused", func(t *testing.T) {
		// The reuse run names a resident hub and creates no hub body, so the id is
		// read. A member id used as a hub is the shape the audit found landing
		// silently.
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(
			landingShapedBatch("", hubTargetMemberA, false)))
		require.Error(t, err, "the landing path runs the hub rules now")
		assert.Contains(t, err.Error(), hubTargetMemberA)
		assert.Contains(t, err.Error(), "not a")
		assert.Empty(t, fc.execMutations, "nothing is written")
	})

	t.Run("a REUSE batch under a REAL hub lands", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(
			landingShapedBatch("", hubEndpointsHubA, false)))
		require.NoError(t, err, "a resident hub is a hub: %v", err)
		assert.Len(t, fc.execMutations, 1)
	})

	t.Run("a hub keyed to ANOTHER node is refused on this route", func(t *testing.T) {
		// THE NESTING RULE, driven where only the engine can refuse it. The tools
		// gate never sees this payload, so a row that went through the intercept
		// would stay green with the engine rule deleted.
		fc := practiceHubTargetFake(t)
		// THE EDGE IS CARRIED, so the body-edge rule has nothing to say and the
		// refusal can only be the nesting rule's. A row without it would be caught
		// by the sibling rule and would stay green with this one deleted.
		_, err := executeMutate(opCtx(), fc, json.RawMessage(
			`{"operation":"create_batch","graph":"practice","nodes":[{"type":"`+string(kgtypes.NodeSource)+
				`","id":"nested-hub","name":"h","summary":"s","metadata":{"`+kgtypes.MetaKeySourceHub+
				`":"`+hubEndpointsHubA+`"}}],"edges":[{"from_idx":0,"to_idx":-1,"to_id":"`+hubEndpointsHubA+
				`","type":"`+string(kgtypes.EdgeSourcedFrom)+`"}]}`))
		require.Error(t, err, "a hub is grouped under nothing but itself")
		assert.Contains(t, err.Error(), hubEndpointsHubA)
		assert.Empty(t, fc.execMutations)
	})

	t.Run("a member body with a hub key and NO edge is refused on this route", func(t *testing.T) {
		// THE BODY-CARRIES-ITS-EDGE RULE, driven the same way and for the same
		// reason. Grouping is the key AND the edge; a batch that writes the key
		// alone files the node under a hub nothing links it to.
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(
			`{"operation":"create_batch","graph":"practice","nodes":[{"type":"pattern","id":"orphaned-1",`+
				`"name":"m","summary":"s","metadata":{"`+kgtypes.MetaKeySourceHub+`":"`+hubEndpointsHubA+`"}}]}`))
		require.Error(t, err, "a body key with no edge is half a grouping")
		assert.Contains(t, err.Error(), string(kgtypes.EdgeSourcedFrom))
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the landing's own hubBody passes the nesting rule", func(t *testing.T) {
		// THE PRODUCER IS THE FIXTURE. hubBody writes the hub's own id under the
		// hub key on purpose — one predicate then sweeps the hub with its members
		// — and an earlier form of this rule refused exactly that, which would
		// have made every landed hub unusable. Driving the producer's own output
		// is what keeps the two designs from contradicting each other again.
		pre := &landingPreflight{hubID: "produced-hub", kind: "web", origin: "example.org", slug: "eip"}
		body := hubBody(pre)
		require.Equal(t, body.ID, body.Metadata[kgtypes.MetaKeySourceHub],
			"the producer writes the self-key this rule must accept")

		raw, merr := json.Marshal(landingBatchArgs{
			Operation: "create_batch", Graph: string(kgtypes.GraphPractice),
			Nodes: []persistBatchNode{body},
		})
		require.NoError(t, merr)
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, raw)
		assert.NoError(t, err, "the shipped producer's hub must be a legal hub")
	})
}

// TestPracticeHubBypass_StandaloneDeleteRunsTheResolver drives the other bypass:
// the standalone `delete` tool, whose `source` is the hub axis under another
// spelling and which reached the by-hub selection with no resolution at all.
func TestPracticeHubBypass_StandaloneDeleteRunsTheResolver(t *testing.T) {
	for _, row := range []struct{ name, hub, want string }{
		{"a member id used as a hub", hubTargetMemberA, "not a"},
		{"a hub that resolves to nothing", hubGhost, "resolves to no node"},
	} {
		t.Run(row.name+" is refused, not deleted", func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "delete", json.RawMessage(
				`{"graph":"practice","source":"`+row.hub+`"}`))
			require.NoError(t, err)
			require.True(t, res.IsError, "the standalone tool runs the same resolver the mutate arms do")
			body := toolResultText(res)
			assert.Contains(t, body, row.hub, "the refusal names the id the caller passed")
			assert.Contains(t, body, row.want, "and says what is wrong with it")
			assert.Contains(t, body, "delete", "and names the tool")
			assert.Empty(t, fc.execMutations, "nothing is deleted — this arm destroyed a node before")
		})
	}

	t.Run("CONTROL: a real hub sweeps", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "delete", json.RawMessage(
			`{"graph":"practice","source":"`+hubEndpointsHubA+`"}`))
		require.NoError(t, err)
		require.False(t, res.IsError, toolResultText(res))
		require.Len(t, fc.execMutations, 1, "and it is ONE write")
		assert.Equal(t, hubEndpointsHubA,
			fc.execMutations[0].GetSelection().GetMetadataPredicates()[0].GetValue(),
			"selected by the hub key, which the hub itself carries")
	})
}

// TestPracticeHubBypass_TheInterceptAdmitsTheLandingShapeToo is the INTERCEPT-path
// counterpart of the landing row above, and it is the observer for the tools
// gate's delegation.
//
// WHY IT IS A SEPARATE ROW. The tools gate used to state the create-side body
// rule in its own words, and its words were wrong: it refused ANY body carrying a
// hub on a hub-less create, which is exactly the shape the shipped landing writes
// and exactly what the audit found. It renders the engine's rule now. Restoring
// the gate's own version reds this row while the bypass rows stay green, which is
// what says the two positions are one rule rather than two.
func TestPracticeHubBypass_TheInterceptAdmitsTheLandingShapeToo(t *testing.T) {
	fc, _, res := driveHubTarget(t, landingShapedBatch("new-hub-2", "new-hub-2", true))
	assert.False(t, res.IsError,
		"the landing's own shape must pass the intercept as well as the funnel: %s", toolResultText(res))
	assert.NotEmpty(t, fc.execMutations, "and it writes")
}
