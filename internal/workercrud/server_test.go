// SPDX-License-Identifier: Apache-2.0

package workercrud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// crudSampleWorker returns a fully populated worker for the lifecycle
// tests. Mirrors sampleWorker() in persist_test.go but lives here so
// server_test.go is self-contained — server vs persist tests can drift
// independently if the fixtures need to.
func crudSampleWorker(name string) workers.Worker {
	return workers.Worker{
		Name:                name,
		Description:         "exercises every persisted field",
		SystemPrompt:        "You are a CRUD-test worker. Do nothing.",
		Provider:            config.ProviderAnthropic,
		Model:               "claude-haiku-4-5-20251001",
		ToolAllowlist:       []string{"search", "think", "mutate"},
		Triggers:            []workers.Trigger{{Event: workers.EventManual}},
		MaxIterations:       12,
		MaxWallclockSeconds: 75,
		Enabled:             true,
	}
}

// TestClient_List_HappyPath asserts that List compiles a type=worker
// query plan, runs it through Execute, and decodes the nodes_json carrier
// into Workers via NodeToWorker.
func TestClient_List_HappyPath(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueNodes(t, []workers.Worker{
		crudSampleWorker("alpha"),
		crudSampleWorker("bravo"),
	})

	got, err := New(fake).List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].Name)
	assert.Equal(t, "bravo", got[1].Name)

	// Compiled-plan assertions: a type=worker browse with the explicit large
	// limit (workerListLimit, NOT the engine default) on the QueryPlan selection.
	require.Len(t, fake.execs, 1)
	q := fake.execs[0].GetQuery()
	require.NotNil(t, q, "List compiles a QueryPlan")
	assert.Equal(t, "worker", q.GetSelection().GetNodeType())
	assert.Equal(t, int32(workerListLimit), q.GetLimit())
}

// TestClient_List_LimitNotZero pins the criterion 9e87d7bc: the compiled
// QueryPlan carries the explicit large limit (workerListLimit) so the worker
// catalog is not silently capped.
func TestClient_List_LimitNotZero(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueNodes(t, nil)

	_, err := New(fake).List(context.Background())
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	q := fake.execs[0].GetQuery()
	require.NotNil(t, q)
	assert.Greater(t, int(q.GetLimit()), 20, "limit must exceed any default cap")
	assert.Equal(t, int32(workerListLimit), q.GetLimit())
}

// TestClient_List_FiftyWorkersReturnedUncapped is the bulk-capacity proof
// from criterion 9e87d7bc: a 50-worker nodes_json carrier round-trips with
// len(got) == 50.
func TestClient_List_FiftyWorkersReturnedUncapped(t *testing.T) {
	fake := &fakeCRUDClient{}
	many := make([]workers.Worker, 50)
	for i := range many {
		many[i] = crudSampleWorker(fmt.Sprintf("w%02d", i))
	}
	fake.queueNodes(t, many)

	got, err := New(fake).List(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 50)
}

// TestClient_List_EmptyEnvelopeReturnsNil pins the empty-catalog contract:
// an empty nodes_json carrier returns (nil, nil) so callers can use a simple
// len check.
func TestClient_List_EmptyEnvelopeReturnsNil(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueNodes(t, nil)

	got, err := New(fake).List(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestClient_List_WireErrorSurfacesPayload pins error propagation from the
// Execute carrier — the underlying error text appears in the returned error.
func TestClient_List_WireErrorSurfacesPayload(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueErr(errors.New("query failed: bad selector"))

	_, err := New(fake).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad selector")
}

// TestClient_ByName_FoundAndMissing exercises both branches of the
// list-then-scan ByName path.
func TestClient_ByName_FoundAndMissing(t *testing.T) {
	fake := &fakeCRUDClient{}
	// Two consecutive ByName calls — queue two node carriers.
	fake.queueNodes(t, []workers.Worker{crudSampleWorker("alpha")})
	fake.queueNodes(t, []workers.Worker{crudSampleWorker("alpha")})

	client := New(fake)

	w, ok, err := client.ByName(context.Background(), "alpha")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "alpha", w.Name)

	_, ok, err = client.ByName(context.Background(), "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestClient_ByName_EmptyShortCircuits pins the contract that
// ByName("") returns (zero, false, nil) without touching the wire.
func TestClient_ByName_EmptyShortCircuits(t *testing.T) {
	fake := &fakeCRUDClient{}
	_, ok, err := New(fake).ByName(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, fake.execs, "ByName(\"\") must not dispatch any wire call")
}

// TestClient_Create_EmitsUpsertArgs pins that Create compiles the expected
// MUTATION_KIND_UPSERT plan (one NodeBody: type=worker, id == name, source
// attribution preserved, metadata carried).
func TestClient_Create_EmitsUpsertArgs(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{"alpha"}})

	err := New(fake).Create(context.Background(), crudSampleWorker("alpha"))
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m, "Create compiles a MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "worker", body.GetType())
	assert.Equal(t, "alpha", body.GetId())
	assert.Equal(t, "alpha", body.GetName())
	// Source attribution — must round-trip "worker:configure" (NOT "llm:claude")
	// so the persisted node's source matches the WorkerToNode invariant.
	assert.Equal(t, "worker:configure", body.GetSource())
	assert.Equal(t, "anthropic", body.GetMetadata()["provider"])
}

// TestClient_Update_EmitsSameUpsertArgs pins that Update compiles the same
// UPSERT plan as Create (upsert is the unified create-or-update path).
func TestClient_Update_EmitsSameUpsertArgs(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{"alpha"}})

	err := New(fake).Update(context.Background(), crudSampleWorker("alpha"))
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	assert.Equal(t, "worker:configure", m.GetNodeBodies()[0].GetSource())
}

// TestClient_Create_WireErrorSurfacesPayload pins error propagation from
// the Execute carrier.
func TestClient_Create_WireErrorSurfacesPayload(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueErr(errors.New("upsert failed: storage offline"))

	err := New(fake).Create(context.Background(), crudSampleWorker("alpha"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage offline")
}

// TestClient_Delete_EmitsDeleteArgs pins that Delete compiles the expected
// MUTATION_KIND_DELETE by-id plan (Selection.Ids carries the worker name).
func TestClient_Delete_EmitsDeleteArgs(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueResp(&knowledgev1.ExecuteResponse{AffectedCount: 1})

	err := New(fake).Delete(context.Background(), "alpha")
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m, "Delete compiles a MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.Equal(t, []string{"alpha"}, m.GetSelection().GetIds())
}

// TestClient_Delete_NotFoundMapsToErrNotFound is the engine-error
// classification contract: a CodeNotFound Execute error maps to a wrapped
// graphclient.ErrNotFound so errors.Is holds for the InterceptWorker delete path.
func TestClient_Delete_NotFoundMapsToErrNotFound(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueErr(connect.NewError(connect.CodeNotFound, errors.New("node alpha not found")))

	err := New(fake).Delete(context.Background(), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, graphclient.ErrNotFound, "Delete should map a CodeNotFound engine error to graphclient.ErrNotFound")
}

// TestClient_Delete_OtherWireErrorSurfacesPayload pins that non-not-found
// engine errors propagate their text verbatim and are NOT classified as
// ErrNotFound.
func TestClient_Delete_OtherWireErrorSurfacesPayload(t *testing.T) {
	fake := &fakeCRUDClient{}
	fake.queueErr(connect.NewError(connect.CodePermissionDenied, errors.New("delete failed: permission denied")))

	err := New(fake).Delete(context.Background(), "alpha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	assert.NotErrorIs(t, err, graphclient.ErrNotFound, "permission-denied error must not be classified as ErrNotFound")
}

// TestClient_NilGraphClient_ReturnsErrors guards the nil-gc construction
// path. Production callers must wire a real client; tests that don't
// inject one should get a clear error.
func TestClient_NilGraphClient_ReturnsErrors(t *testing.T) {
	c := New(nil)
	_, err := c.List(context.Background())
	require.Error(t, err)
	require.Error(t, c.Create(context.Background(), crudSampleWorker("a")))
	require.Error(t, c.Update(context.Background(), crudSampleWorker("a")))
	require.Error(t, c.Delete(context.Background(), "a"))
}
