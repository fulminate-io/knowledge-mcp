// SPDX-License-Identifier: Apache-2.0

package engine

// compile_mutate_update_batch_test.go holds the update_batch contract suite,
// moved OUT of compile_mutate_test.go for the repository's 500-line per-file cap.
//
// THE SUITE MOVED WHOLE rather than the new cases being started elsewhere. Both
// of its tests are one contract read from two angles — what a payload compiles
// TO, and what that compiled plan round-trips as on the wire — so splitting them
// across files would leave a reader who found one unaware the other exists,
// which is exactly what "extend the existing suite, do not start a new file"
// was written to prevent.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCompileMutate_UpdateBatch covers the update_batch contract: a
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

	// A PER-ITEM DESCRIPTION REACHES THE WIRE. This is the whole point of the
	// carrier: before the proto field existed the key died at json.Unmarshal, the
	// item compiled to the id ALONE, and the call returned success having written
	// none of it.
	t.Run("a per-item description compiles onto the UpdateItem", func(t *testing.T) {
		args := `{"operation":"update_batch","items":[{"id":"n1","description":"a new section body"}]}`
		req, ok := compileMutate(json.RawMessage(args))
		require.True(t, ok)
		items := req.GetMutation().GetUpdateItems()
		require.Len(t, items, 1)
		assert.Equal(t, "n1", items[0].GetId())
		require.NotNil(t, items[0].Description, "the description must reach the plan, not be dropped")
		assert.Equal(t, "a new section body", items[0].GetDescription())
		// THE POINTER CONTRACT IS PRESERVED: the fields the caller did not name
		// stay nil, which is what "untouched" means on this arm. A carrier that
		// materialized empty strings would clear them instead.
		assert.Nil(t, items[0].Summary, "an unnamed field stays nil — untouched, not cleared")
		assert.Nil(t, items[0].Keywords)
		assert.Nil(t, items[0].Status)
	})

	// PER-ITEM HETEROGENEITY, at the compile layer: two items with different
	// bodies compile to two distinct UpdateItems. One item cannot distinguish a
	// per-item carrier from a batch-wide one.
	//
	// THIS IS THE CLIENT'S WIRE BOUNDARY, and the assertion belongs here rather
	// than one layer up: an ACCEPTED update_batch is not executed by
	// InterceptMutate at all — the guard claims only a REFUSAL, and an accepted
	// call falls through to the engine arm with handled=false. So the compiled
	// MutationPlan below IS what reaches the server, and a tools-layer fake sees
	// no Execute to inspect. The server half of this seam is
	// TestExecuteMutation_UpdateItemsAppliesDistinctDescriptions in the server's
	// bootstrap package; the two modules share no hand-written package, so the
	// generated proto type both import is the seam's only common artifact.
	t.Run("two items carry two distinct descriptions", func(t *testing.T) {
		args := `{"operation":"update_batch","items":[` +
			`{"id":"sec-0","description":"body zero"},{"id":"sec-1","description":"body one"}]}`
		req, ok := compileMutate(json.RawMessage(args))
		require.True(t, ok)
		items := req.GetMutation().GetUpdateItems()
		require.Len(t, items, 2)
		assert.Equal(t, "body zero", items[0].GetDescription())
		assert.Equal(t, "body one", items[1].GetDescription())
	})

	// A PRESENT-BUT-EMPTY DESCRIPTION IS A DELIBERATE CLEAR, not an omission —
	// the same set/unset distinction summary and status carry. A carrier that
	// collapsed the two would make clearing a body impossible to express.
	t.Run("an empty description is a set, not an omission", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update_batch","items":[{"id":"n1","description":""}]}`))
		require.True(t, ok)
		items := req.GetMutation().GetUpdateItems()
		require.Len(t, items, 1)
		require.NotNil(t, items[0].Description, "present-and-empty is SET")
		assert.Empty(t, items[0].GetDescription())
	})

	// THE UNDECLARED-KEY CLASS IS UNCHANGED, and it is characterized here on a key
	// that is STILL undeclared. `description` left this class by gaining a
	// carrier; every other unrecognized key is still discarded at
	// json.Unmarshal, which is why the payload-shape guard at the mutate dispatch
	// seam (guardUpdateBatchItemKeys, package tools) still exists and still has
	// work to do.
	t.Run("an undeclared item key is still gone by compile time", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"update_batch","items":[{"id":"n1","note":"not a field"}]}`))
		require.True(t, ok, "the call compiles: nothing here can see the dropped key")
		items := req.GetMutation().GetUpdateItems()
		require.Len(t, items, 1)
		assert.Equal(t, "n1", items[0].GetId())
		assert.Nil(t, items[0].Description, "an undeclared key is nowhere on the plan")
		assert.Nil(t, items[0].Summary)
		assert.Empty(t, items[0].GetMetadata())
	})
}

// TestCompileMutate_UpdateBatchBranch is the wire-contract round trip between
// the pipeline's overlay-qualified writeback and the engine compiler: a
// mutate(update_batch, graph:code, repo:myrepo, branch:feat) must thread the
// branch onto the Execute Target so the server resolveCode Scopes the overlay
// (repo@branch) instead of resolving the base graph. The branch json tag must
// be exactly "branch" — the same tag rpc.go's updateBatchArgs marshals — or the
// overlay dimension is silently dropped and overlay-resident writebacks fail
// not_found.
func TestCompileMutate_UpdateBatchBranch(t *testing.T) {
	t.Run("branch threads onto the Execute Target", func(t *testing.T) {
		// Marshal the SAME wire shape rpc.go's updateBatchArgs produces (the json
		// tags are the contract), through the public engine.Compile entrypoint.
		args, err := json.Marshal(map[string]any{
			"operation": "update_batch",
			"graph":     "code",
			"repo":      "myrepo",
			"branch":    "feat",
			"items":     []map[string]any{{"id": "a", "summary": "sum-a"}},
		})
		require.NoError(t, err)
		req, ok := Compile("mutate", args)
		require.True(t, ok, "update_batch with branch lowers to UPDATE_ITEMS")
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "code", req.GetTarget().GetGraph())
		assert.Equal(t, "myrepo", req.GetTarget().GetRepo())
		assert.Equal(t, "feat", req.GetTarget().GetBranch(),
			"branch must thread onto the Target so resolveCode Scopes the overlay")
	})

	t.Run("absent branch → empty Target.Branch (base/default-branch write)", func(t *testing.T) {
		args := `{"operation":"update_batch","graph":"code","repo":"myrepo","items":[{"id":"a","summary":"s"}]}`
		req, ok := Compile("mutate", json.RawMessage(args))
		require.True(t, ok)
		require.NotNil(t, req.GetTarget())
		assert.Empty(t, req.GetTarget().GetBranch(), "no branch → base graph write, Branch stays empty")
	})
}
