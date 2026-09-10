// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mintedHubIDShape is the shape mintPracticeHubID owes: 128 bits of lowercase
// hex, the same shape the store assigns an id-less body. It is asserted because
// the self-key equality alone holds for ANY value the minter returns, constant
// or unquotable included.
const mintedHubIDShape = `^[0-9a-f]{32}$`

// TestPracticeHubSelfKey_StampedOnEveryCreatedHub is the invariant the by-hub
// delete rests on, and it is an ENGINE stamp rather than a caller convention.
//
// WHY IT HAD TO MOVE. The by-hub delete is one metadata predicate — every
// practice node whose `source_hub` equals H — and it sweeps the hub itself only
// because the hub carries its own id under that key. The recipe landing and the
// migration driver both wrote that key by hand, so every hub in the corpus had
// it and the convention looked like an invariant. It was not: a hub created by
// mutate(create, graph:"practice", type:"source") has a SERVER-ASSIGNED id and
// no key at all, and a by-hub delete removed its members and left it behind as
// the empty shell the arm exists to remove.
//
// SO THE ENGINE STAMPS IT, at the one position every caller passes, and mints the
// id when the caller supplied none — because a node cannot carry its own id
// under a key before that id exists.
func TestPracticeHubSelfKey_StampedOnEveryCreatedHub(t *testing.T) {
	for _, row := range []struct {
		name, payload string
		minted        bool
	}{
		{"a single create with NO id", `{"operation":"create","graph":"practice","type":"` +
			string(kgtypes.NodeSource) + `","name":"H","summary":"h"}`, true},
		{"a single create with a caller id", `{"operation":"create","graph":"practice","type":"` +
			string(kgtypes.NodeSource) + `","id":"caller-hub","name":"H","summary":"h"}`, false},
		{"a create_batch with NO id", `{"operation":"create_batch","graph":"practice","nodes":[{"type":"` +
			string(kgtypes.NodeSource) + `","name":"H","summary":"h"}]}`, true},
		{"a create_batch with a caller id", `{"operation":"create_batch","graph":"practice","nodes":[{"type":"` +
			string(kgtypes.NodeSource) + `","id":"batch-hub","name":"H","summary":"h"}]}`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			req, ok := Compile("mutate", json.RawMessage(row.payload))
			require.True(t, ok, "the shape must compile")
			bodies := req.GetMutation().GetNodeBodies()
			require.Len(t, bodies, 1)

			id := bodies[0].GetId()
			require.NotEmpty(t, id,
				"a hub needs an id at compile time: it cannot carry its own id under the hub key otherwise")
			assert.Equal(t, id, bodies[0].GetMetadata()[kgtypes.MetaKeySourceHub],
				"every created hub carries its OWN id under %s, which is what makes the by-hub delete sweep it",
				kgtypes.MetaKeySourceHub)
			if row.minted {
				assert.Regexp(t, mintedHubIDShape, id,
					"a minted id is 128 bits of lowercase hex, the shape the store assigns: an id of any "+
						"other shape is not one a caller could have quoted back, and the equality above "+
						"holds for any constant the minter might return")
			}
		})
	}

	t.Run("TWO id-less hubs in ONE batch get DISTINCT minted ids", func(t *testing.T) {
		// THE MINT IS PER BODY, NOT PER CALL. One value reused across the batch would
		// key both hubs to the same id, and a by-hub delete of either would take the
		// other and its members with it.
		req, ok := Compile("mutate", json.RawMessage(
			`{"operation":"create_batch","graph":"practice","nodes":[{"type":"`+string(kgtypes.NodeSource)+
				`","name":"H1","summary":"h"},{"type":"`+string(kgtypes.NodeSource)+
				`","name":"H2","summary":"h"}]}`))
		require.True(t, ok, "the shape must compile")
		bodies := req.GetMutation().GetNodeBodies()
		require.Len(t, bodies, 2)
		for i, b := range bodies {
			assert.Regexp(t, mintedHubIDShape, b.GetId(), "body %d carries a minted id", i)
			assert.Equal(t, b.GetId(), b.GetMetadata()[kgtypes.MetaKeySourceHub],
				"body %d reads back keyed to ITSELF", i)
		}
		assert.NotEqual(t, bodies[0].GetId(), bodies[1].GetId(),
			"two hubs minted in one call are two hubs")
	})

	t.Run("a MEMBER is untouched: only a source node is self-keyed", func(t *testing.T) {
		req, ok := Compile("mutate", json.RawMessage(
			`{"operation":"create","graph":"practice","type":"pattern","id":"m-1","name":"m","summary":"s"}`))
		require.True(t, ok)
		assert.Empty(t, req.GetMutation().GetNodeBodies()[0].GetMetadata()[kgtypes.MetaKeySourceHub],
			"a member's hub comes from the call's parameter, never from its own id")
	})

	t.Run("another FAMILY is untouched", func(t *testing.T) {
		req, ok := Compile("mutate", json.RawMessage(
			`{"operation":"create","graph":"checks","type":"`+string(kgtypes.NodeSource)+
				`","id":"c-1","name":"H","summary":"h"}`))
		require.True(t, ok)
		assert.Empty(t, req.GetMutation().GetNodeBodies()[0].GetMetadata()[kgtypes.MetaKeySourceHub],
			"no family but practice has source hubs")
	})

	t.Run("a source body keyed to ANOTHER node in a BATCH is refused, naming both", func(t *testing.T) {
		// THE BATCH BRANCH, which is where the landing's bodies live. The body-edge
		// rule must skip a `source` body here as it does at the top level, or it
		// speaks first and names an absent edge rather than the hub the caller
		// typed.
		err := GuardPracticePayload(json.RawMessage(
			`{"operation":"create_batch","graph":"practice","nodes":[{"type":"` + string(kgtypes.NodeSource) +
				`","id":"hub-a","name":"H","summary":"h","metadata":{"` + kgtypes.MetaKeySourceHub +
				`":"hub-b"}}]}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hub-a", "the refusal names the hub being created")
		assert.Contains(t, err.Error(), "hub-b", "and the hub it was wrongly keyed to")
	})

	t.Run("a source body keyed to ANOTHER node is refused, naming both", func(t *testing.T) {
		err := GuardPracticePayload(json.RawMessage(
			`{"operation":"create","graph":"practice","type":"` + string(kgtypes.NodeSource) +
				`","id":"hub-a","name":"H","summary":"h","metadata":{"` + kgtypes.MetaKeySourceHub +
				`":"hub-b"}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hub-a")
		assert.Contains(t, err.Error(), "hub-b")
	})
}

// TestPracticeHubOffFamily_ReadsBothSpellings closes the axis the audit found
// open: the hub selector is spelled `source_hub` on the mutate arms and `source`
// on the standalone delete tool, and the family gate saw only the first.
//
// WHAT THAT COST. delete{graph:"knowledge", source:X} compiled a real
// metadata-predicate DELETE against the knowledge graph and reported success —
// the same defect this branch closed for the mutate spelling, reached through the
// one spelling the delete tool actually publishes.
func TestPracticeHubOffFamily_ReadsBothSpellings(t *testing.T) {
	writes := 0
	var exec ExecuteFn = func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		if _, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
			writes++
		}
		return &knowledgev1.ExecuteResponse{AffectedCount: 7}, nil
	}
	var stats StatsFn = func(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
		return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
	}

	for _, row := range []struct{ name, payload string }{
		{"graph knowledge", `{"graph":"knowledge","source":"ghost-hub"}`},
		{"graph omitted", `{"source":"ghost-hub"}`},
		{"graph checks", `{"graph":"checks","source":"ghost-hub"}`},
	} {
		t.Run(row.name+" is refused", func(t *testing.T) {
			writes = 0
			res, err := Dispatch(context.Background(), exec, stats, "delete", json.RawMessage(row.payload))
			require.NoError(t, err)
			require.True(t, res.IsError, "the hub selector on a family with no hubs is refused")
			require.NotEmpty(t, res.Content)
			assert.Contains(t, res.Content[0].Text, "source",
				"the refusal names the spelling the caller used")
			assert.Contains(t, res.Content[0].Text, "delete",
				"and the tool, never a blank operation")
			assert.Zero(t, writes, "and nothing is written")
		})
	}

	t.Run("CONTROL: the practice family still sweeps", func(t *testing.T) {
		writes = 0
		hub := &knowledgev1.Node{
			Id: "real-hub", Type: string(kgtypes.NodeSource),
			Metadata: map[string]string{kgtypes.MetaKeySourceHub: "real-hub"},
		}
		var seeded ExecuteFn = func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			if q, isQuery := req.GetPlan().(*knowledgev1.ExecuteRequest_Query); isQuery && len(q.Query.GetIds()) > 0 {
				return enginetest.ResponseWithNodes(hub), nil
			}
			if _, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
				writes++
			}
			return &knowledgev1.ExecuteResponse{AffectedCount: 3}, nil
		}
		res, err := Dispatch(context.Background(), seeded, stats, "delete", json.RawMessage(
			`{"graph":"practice","source":"real-hub"}`))
		require.NoError(t, err)
		assert.False(t, res.IsError, "the family the selector belongs to is untouched")
		assert.Equal(t, 1, writes)
	})
}
