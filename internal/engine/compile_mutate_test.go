// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestCompileMutate_CreateSingle(t *testing.T) {
	req, ok := compileMutate(json.RawMessage(`{"operation":"create","type":"document","name":"Doc","summary":"s","metadata":{"k":"v"}}`))
	require.True(t, ok)
	m := req.GetMutation()
	require.NotNil(t, m)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1, "single create → one NodeBody")
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "document", body.GetType())
	assert.Equal(t, "Doc", body.GetName())
	assert.Equal(t, "s", body.GetSummary())
	assert.Equal(t, map[string]string{"k": "v"}, body.GetMetadata())
}

func TestCompileMutate_CreateBatchOnePlan(t *testing.T) {
	args := `{"operation":"create_batch",
		"nodes":[{"type":"finding","name":"A","summary":"a"},{"type":"decision","name":"B","summary":"b"}],
		"edges":[{"from_idx":0,"to_idx":1,"type":"informed-by"},{"from_idx":1,"to_id":"existing","type":"relates-to"}]}`
	req, ok := compileMutate(json.RawMessage(args))
	require.True(t, ok)
	m := req.GetMutation()
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 2, "N NodeBodies in ONE plan")
	require.Len(t, m.GetEdges(), 2, "M edges in the same plan")
	// First edge: slot→slot.
	assert.Equal(t, int32(0), m.GetEdges()[0].GetFromIdx())
	assert.Equal(t, int32(1), m.GetEdges()[0].GetToIdx())
	// Second edge: slot→existing id (to_idx absent → -1 sentinel).
	assert.Equal(t, int32(1), m.GetEdges()[1].GetFromIdx())
	assert.Equal(t, int32(-1), m.GetEdges()[1].GetToIdx(), "absent to_idx → -1 sentinel")
	assert.Equal(t, "existing", m.GetEdges()[1].GetToId())
}

// TestCompileMutate_CreateBatchEdgeMetadata covers the BatchEdgeSpec edge-metadata
// carriers (T-GTB1d P2/P4): a create_batch edge carrying weight / confidence /
// method / evidence / last_validated (RFC3339, sub-second) compiles to a
// BatchEdgeSpec with those fields populated, with last_validated converted to
// int64 unix-nanos.
func TestCompileMutate_CreateBatchEdgeMetadata(t *testing.T) {
	args := `{"operation":"create_batch",
		"nodes":[{"type":"finding","name":"A","summary":"a"},{"type":"decision","name":"B","summary":"b"}],
		"edges":[{"from_idx":0,"to_idx":1,"type":"relates-to","weight":2.5,"confidence":0.75,"method":"manual","evidence":"file.go:42","last_validated":"2026-05-23T12:00:00.123456789Z"}]}`
	req, ok := compileMutate(json.RawMessage(args))
	require.True(t, ok)
	m := req.GetMutation()
	require.Len(t, m.GetEdges(), 1)

	e := m.GetEdges()[0]
	assert.InDelta(t, 2.5, e.GetWeight(), 1e-9)
	assert.InDelta(t, 0.75, e.GetConfidence(), 1e-9)
	assert.Equal(t, "manual", e.GetMethod())
	assert.Equal(t, "file.go:42", e.GetEvidence())

	// last_validated RFC3339Nano → int64 unix-nanos (full sub-second fidelity).
	want, perr := time.Parse(time.RFC3339, "2026-05-23T12:00:00.123456789Z")
	require.NoError(t, perr)
	assert.Equal(t, want.UnixNano(), e.GetLastValidated(),
		"last_validated must convert to unix-nanos preserving sub-second precision")
}

// TestCompileMutate_CreateBatchMalformedLastValidated pins that an unparseable
// last_validated falls through to legacy (ok=false) so the RFC3339 error surfaces
// there — matching the LINK-arm contract.
func TestCompileMutate_CreateBatchMalformedLastValidated(t *testing.T) {
	args := `{"operation":"create_batch",
		"nodes":[{"type":"finding","name":"A","summary":"a"}],
		"edges":[{"from_idx":0,"to_id":"x","type":"relates-to","last_validated":"not-a-timestamp"}]}`
	_, ok := compileMutate(json.RawMessage(args))
	assert.False(t, ok, "malformed last_validated → fall through to legacy")
}

// TestCompileMutate_CreateBatchBundleID covers the MutationPlan.bundle_id carrier
// (T-GTB1d P4): a create_batch with bundle_id produces a MutationPlan carrying
// that bundle_id (the engine wraps the mutation ctx with it). Absent bundle_id
// leaves the carrier empty.
func TestCompileMutate_CreateBatchBundleID(t *testing.T) {
	t.Run("bundle_id rides the plan", func(t *testing.T) {
		args := `{"operation":"create_batch","bundle_id":"bundle-xyz",
			"nodes":[{"type":"finding","name":"A","summary":"a"}]}`
		req, ok := compileMutate(json.RawMessage(args))
		require.True(t, ok)
		assert.Equal(t, "bundle-xyz", req.GetMutation().GetBundleId())
	})

	t.Run("absent bundle_id leaves the carrier empty", func(t *testing.T) {
		args := `{"operation":"create_batch",
			"nodes":[{"type":"finding","name":"A","summary":"a"}]}`
		req, ok := compileMutate(json.RawMessage(args))
		require.True(t, ok)
		assert.Empty(t, req.GetMutation().GetBundleId())
	})
}

// TestCompileMutate_ByIDArmsReduce asserts the by-id WRITE arms now COMPILE to
// Execute (T2.4c closed TICKET-GAP b535d1a9 by adding the Selection.ids by-id
// write selector): an id-targeted update / a delete over an id-set / a by-id
// link/unlink (from→to) each lower to a MutationPlan with Selection.Ids carrying
// the literal target set (NOT a traversal anchor — the old wrong-set bug).
func TestCompileMutate_ByIDArmsReduce(t *testing.T) {
	t.Run("update by id → UPDATE with Selection.Ids + set_fields", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update","id":"n1","status":"closed"}`))
		require.True(t, ok)
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
		assert.Equal(t, []string{"n1"}, m.GetSelection().GetIds())
		assert.Equal(t, "closed", m.GetSetFields()["status"])
	})

	t.Run("update metadata → set_metadata", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update","id":"n1","metadata":{"k":"v"}}`))
		require.True(t, ok)
		m := req.GetMutation()
		assert.Equal(t, []string{"n1"}, m.GetSelection().GetIds())
		assert.Equal(t, map[string]string{"k": "v"}, m.GetSetMetadata())
	})

	t.Run("delete by ids → DELETE with Selection.Ids", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"delete","ids":["a","b"]}`))
		require.True(t, ok)
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
		assert.Equal(t, []string{"a", "b"}, m.GetSelection().GetIds())
	})

	t.Run("link single → LINK with Selection.Ids[from] + EdgeSpec", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"link","from":"x","to":"y","relationship":"informed-by"}`))
		require.True(t, ok)
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, m.GetKind())
		assert.Equal(t, []string{"x"}, m.GetSelection().GetIds())
		assert.Equal(t, "informed-by", m.GetEdgeSpec().GetRelationship())
		assert.Equal(t, "y", m.GetEdgeSpec().GetToId())
		assert.True(t, m.GetEdgeSpec().GetForward(), "the from node is the edge SOURCE")
	})

	t.Run("unlink single → UNLINK", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"unlink","from":"x","to":"y","relationship":"relates-to"}`))
		require.True(t, ok)
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UNLINK, m.GetKind())
		assert.Equal(t, []string{"x"}, m.GetSelection().GetIds())
		assert.Equal(t, "relates-to", m.GetEdgeSpec().GetRelationship())
	})

	// update_batch now COMPILES to the heterogeneous MUTATION_KIND_UPDATE_ITEMS
	// arm (T-GTB1c added the per-item carrier): each item becomes a distinct
	// UpdateItem, no longer falling through to legacy.
	t.Run("update_batch → MUTATION_KIND_UPDATE_ITEMS", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update_batch","items":[{"id":"a","status":"completed"},{"id":"b","status":"completed"}]}`))
		require.True(t, ok, "update_batch lowers to the per-item UPDATE_ITEMS arm")
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, m.GetKind())
		require.Len(t, m.GetUpdateItems(), 2)
	})
}

// TestCompileMutate_Upsert covers criterion 9836ac0d: a mutate(upsert) with id +
// type compiles to a MUTATION_KIND_UPSERT one-body plan carrying id + source +
// the body fields; a mutate(upsert) with an empty id returns ok=false (legacy
// fall-through).
func TestCompileMutate_Upsert(t *testing.T) {
	t.Run("upsert with id+type → MUTATION_KIND_UPSERT one-body plan", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"upsert","type":"proxy","id":"proxy:practice:go:n1","source":"proxy:practice:go","metadata":{"language":"go"}}`))
		require.True(t, ok)
		m := req.GetMutation()
		require.NotNil(t, m)
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, m.GetKind())
		require.Len(t, m.GetNodeBodies(), 1, "upsert → one NodeBody")
		body := m.GetNodeBodies()[0]
		assert.Equal(t, "proxy:practice:go:n1", body.GetId(), "id rides as the upsert key")
		assert.Equal(t, "proxy", body.GetType())
		assert.Equal(t, "proxy:practice:go", body.GetSource(), "source rides as-given")
		assert.Equal(t, map[string]string{"language": "go"}, body.GetMetadata())
	})

	t.Run("upsert with empty id → ok=false (legacy fall-through)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"upsert","type":"worker","name":"w"}`))
		assert.False(t, ok, "missing upsert key → legacy")
		assert.Nil(t, req)
	})

	t.Run("upsert with empty type → ok=false (legacy fall-through)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"upsert","id":"x"}`))
		assert.False(t, ok, "missing type → legacy")
		assert.Nil(t, req)
	})
}

// TestCompileMutate_UpdateBatch covers criterion 67ed6b6f: a
// mutate(update_batch, graph:code, repo:knowledge, items:[{id,summary},
// {id,binary_vector}]) compiles to a MUTATION_KIND_UPDATE_ITEMS plan (2
// UpdateItems, set/unset preserved) with Target{Graph:code, Repo:knowledge};
// empty items → ok=false.
func TestCompileMutate_UpdateBatch(t *testing.T) {
	t.Run("update_batch graph:code repo:knowledge → UPDATE_ITEMS + Target", func(t *testing.T) {
		// 32 base64-encoded bytes for the binary_vector item (decodes to the
		// 256-bit embedding length the engine validates).
		args := `{"operation":"update_batch","graph":"code","repo":"knowledge","items":[` +
			`{"id":"a","summary":"sum-a"},` +
			`{"id":"b","binary_vector":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}]}`
		req, ok := compileMutate(json.RawMessage(args))
		require.True(t, ok)
		m := req.GetMutation()
		require.NotNil(t, m)
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, m.GetKind())
		require.Len(t, m.GetUpdateItems(), 2)

		// Item 0: summary SET, every other field unset (nil pointers preserved).
		assert.Equal(t, "a", m.GetUpdateItems()[0].GetId())
		require.NotNil(t, m.GetUpdateItems()[0].Summary, "summary pointer is set")
		assert.Equal(t, "sum-a", m.GetUpdateItems()[0].GetSummary())
		assert.Nil(t, m.GetUpdateItems()[0].Keywords, "keywords stays unset")
		assert.Nil(t, m.GetUpdateItems()[0].Status, "status stays unset")
		assert.Empty(t, m.GetUpdateItems()[0].GetBinaryVector(), "binary_vector stays unset")

		// Item 1: binary_vector SET, summary unset.
		assert.Equal(t, "b", m.GetUpdateItems()[1].GetId())
		assert.Nil(t, m.GetUpdateItems()[1].Summary, "summary stays unset on the vector item")
		assert.Len(t, m.GetUpdateItems()[1].GetBinaryVector(), 32, "binary_vector decoded to 32 bytes")

		// Target carries the per-graph routing.
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "code", req.GetTarget().GetGraph())
		assert.Equal(t, "knowledge", req.GetTarget().GetRepo())
	})

	t.Run("empty items → ok=false (legacy fall-through)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update_batch","items":[]}`))
		assert.False(t, ok, "empty update_batch → legacy")
		assert.Nil(t, req)
	})
}

// TestCompileMutate_ThoughtChargeCreateCompiles is the T-GTB6 Phase 7 reversal
// of the old default-deny: thought/charge creates now COMPILE to a CREATE
// MutationPlan (the client composers lower think/charge into a generic
// create_batch carrying type=thought/charge NodeBodies + edges, so the engine no
// longer denies them). A direct LLM mutate(create,type:thought|charge) compiles
// too. The former payload-arg deny (weight/branches_from/etc. on a non-thought
// type) is gone — those args are simply ignored on a generic create.
func TestCompileMutate_ThoughtChargeCreateCompiles(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"type thought", `{"operation":"create","type":"thought","content":"c"}`},
		{"type charge", `{"operation":"create","type":"charge","content":"r"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileMutate(json.RawMessage(tc.args))
			require.True(t, ok, "%s now compiles to a CREATE MutationPlan", tc.name)
			assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, req.GetMutation().GetKind())
		})
	}
}

func TestCompileMutate_DenyCases(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		// NOTE: practice/transformers create/update/delete/link/update_batch are NO
		// LONGER here — T-GTB6 Phase 1 narrowed the compileMutate guard to
		// link_graph-only, so an intra-practice/transformers op (no link_graph)
		// Target-routes to a MutationPlan (proven by TestCompileMutate_PracticeTransformers).
		// Only the cross-graph link_graph case stays denied (T-GTB5/legacy).
		{"link_graph linkage", `{"operation":"link","link_graph":"linkage","from":"x","to":"y","relationship":"r"}`},
		{"empty update_batch", `{"operation":"update_batch","items":[]}`},
		{"upsert", `{"operation":"upsert","type":"worker","name":"w"}`},
		{"answer", `{"operation":"answer","id":"q","conclusion":"done"}`},
		// Degenerate bulk_update_metadata shapes still fall through to legacy.
		{"bulk_update_metadata empty", `{"operation":"bulk_update_metadata","updates":[]}`},
		{"bulk_update_metadata no id", `{"operation":"bulk_update_metadata","updates":[{"metadata":{"k":"v"}}]}`},
		{"bulk_update_metadata no metadata", `{"operation":"bulk_update_metadata","updates":[{"id":"a"}]}`},
		{"update no fields", `{"operation":"update","id":"n1"}`},
		{"delete no ids", `{"operation":"delete"}`},
		{"link incomplete", `{"operation":"link","from":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileMutate(json.RawMessage(tc.args))
			assert.False(t, ok, "%s must fall through to legacy", tc.name)
			assert.Nil(t, req)
		})
	}
}

// TestCompileMutate_BulkUpdateMetadata asserts mutate(bulk_update_metadata)
// compiles to a MUTATION_KIND_UPDATE_ITEMS plan with one metadata-only UpdateItem
// per {id, metadata} entry (T-GTB1e #6).
func TestCompileMutate_BulkUpdateMetadata(t *testing.T) {
	req, ok := compileMutate(json.RawMessage(
		`{"operation":"bulk_update_metadata","updates":[{"id":"a","metadata":{"k1":"v1"}},{"id":"b","metadata":{"k2":"v2"}}]}`))
	require.True(t, ok, "bulk_update_metadata must compile to Execute")
	mp := req.GetMutation()
	require.NotNil(t, mp)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, mp.GetKind())
	items := mp.GetUpdateItems()
	require.Len(t, items, 2)
	assert.Equal(t, "a", items[0].GetId())
	assert.Equal(t, map[string]string{"k1": "v1"}, items[0].GetMetadata())
	assert.Equal(t, "b", items[1].GetId())
	assert.Equal(t, map[string]string{"k2": "v2"}, items[1].GetMetadata())
	// Metadata-only: no summary/keywords/status set (nil → untouched).
	assert.Nil(t, items[0].Summary)
	assert.Nil(t, items[0].Status)
}
