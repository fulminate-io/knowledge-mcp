// SPDX-License-Identifier: Apache-2.0

// worker_crud_test.go — split from worker_test.go to keep both files
// under the 500-line cap. Owns the fakeCRUD fixture and tests covering
// the list/create/update/delete CRUD operations.

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

type assertableError string

func (e assertableError) Error() string { return string(e) }

// fakeCRUD satisfies WorkerCRUDAPI without a real wire-loopback Client.
// Records every mutation so tests can pin the per-op behavior; tests
// that need a specific return value can override the listErr / createErr
// fields.
type fakeCRUD struct {
	mu        sync.Mutex
	graph     map[string]workers.Worker
	creates   []workers.Worker
	updates   []workers.Worker
	deletes   []string
	listErr   error
	createErr error
	updateErr error
	deleteErr error
}

func (f *fakeCRUD) List(_ context.Context) ([]workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]workers.Worker, 0, len(f.graph))
	for _, w := range f.graph {
		out = append(out, w)
	}
	return out, nil
}

func (f *fakeCRUD) ByName(_ context.Context, name string) (workers.Worker, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.graph[name]
	return w, ok, nil
}

func (f *fakeCRUD) Create(_ context.Context, w workers.Worker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.creates = append(f.creates, w)
	if f.graph == nil {
		f.graph = map[string]workers.Worker{}
	}
	f.graph[w.Name] = w
	return nil
}

func (f *fakeCRUD) Update(_ context.Context, w workers.Worker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updates = append(f.updates, w)
	if f.graph == nil {
		f.graph = map[string]workers.Worker{}
	}
	f.graph[w.Name] = w
	return nil
}

func (f *fakeCRUD) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, name)
	delete(f.graph, name)
	return nil
}

// validCreateArgs returns a JSON payload that passes Worker.Validate so
// the create-handler tests can focus on the dispatch path without
// re-deriving the validation surface.
const validCreateArgs = `{
	"operation": "create",
	"name": "alpha",
	"system_prompt": "you are a test worker",
	"provider": "anthropic",
	"model": "claude-haiku-4-5-20251001",
	"tool_allowlist": ["search"],
	"max_iterations": 4,
	"max_wallclock_seconds": 30,
	"enabled": true
}`

// TestInterceptWorker_ListEmpty pins the empty-catalog response so the
// operator gets a hint instead of an opaque empty list.
func TestInterceptWorker_ListEmpty(t *testing.T) {
	deps := workerTestDeps{crud: &fakeCRUD{}}
	handled, body, isErr := callWorker(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr)
	assert.Contains(t, body, "No workers registered")
}

// TestInterceptWorker_ListHappyPathJSON pins the JSON output shape: the
// canonical fields surface for every persisted worker.
func TestInterceptWorker_ListHappyPathJSON(t *testing.T) {
	crud := &fakeCRUD{graph: map[string]workers.Worker{
		"alpha": crudSampleWorkerForTest("alpha"),
		"bravo": crudSampleWorkerForTest("bravo"),
	}}
	deps := workerTestDeps{crud: crud}
	handled, body, isErr := callWorker(t, deps, `{"operation":"list","format":"json"}`)
	require.True(t, handled)
	require.False(t, isErr)
	assert.Contains(t, body, "alpha")
	assert.Contains(t, body, "bravo")
}

// TestInterceptWorker_CreateHappyPath pins that a valid create payload
// reaches the CRUD client and produces the "created" response.
func TestInterceptWorker_CreateHappyPath(t *testing.T) {
	crud := &fakeCRUD{}
	deps := workerTestDeps{crud: crud}
	handled, body, isErr := callWorker(t, deps, validCreateArgs)
	require.True(t, handled)
	require.False(t, isErr, "expected non-error, got: %s", body)
	assert.Contains(t, body, "created")
	require.Len(t, crud.creates, 1)
	assert.Equal(t, "alpha", crud.creates[0].Name)
}

// TestInterceptWorker_CreateValidatesWorker pins that Worker.Validate
// runs at the dispatch layer — invalid payloads error out before
// reaching the CRUD client.
func TestInterceptWorker_CreateValidatesWorker(t *testing.T) {
	crud := &fakeCRUD{}
	deps := workerTestDeps{crud: crud}
	handled, body, isErr := callWorker(t, deps, `{"operation":"create","name":"alpha","provider":"anthropic","model":"claude-haiku-4-5","tool_allowlist":["search"]}`)
	require.True(t, handled)
	require.True(t, isErr, "missing system_prompt must surface as error")
	assert.Contains(t, body, "SystemPrompt")
	assert.Empty(t, crud.creates, "Create must not be called on validation failure")
}

// TestInterceptWorker_CreateEmptyName pins the empty-name reject.
func TestInterceptWorker_CreateEmptyName(t *testing.T) {
	deps := workerTestDeps{crud: &fakeCRUD{}}
	handled, body, isErr := callWorker(t, deps, `{"operation":"create","name":""}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "name is required")
}

// TestInterceptWorker_UpdateHappyPath pins the update dispatch path.
func TestInterceptWorker_UpdateHappyPath(t *testing.T) {
	crud := &fakeCRUD{graph: map[string]workers.Worker{"alpha": crudSampleWorkerForTest("alpha")}}
	deps := workerTestDeps{crud: crud}
	args := `{"operation":"update","name":"alpha","system_prompt":"you are updated","provider":"anthropic","model":"claude-haiku-4-5","tool_allowlist":["search"],"max_iterations":4,"max_wallclock_seconds":30,"enabled":false}`
	handled, body, isErr := callWorker(t, deps, args)
	require.True(t, handled)
	require.False(t, isErr, "expected non-error, got: %s", body)
	assert.Contains(t, body, "updated")
	require.Len(t, crud.updates, 1)
	assert.Equal(t, "alpha", crud.updates[0].Name)
	assert.False(t, crud.updates[0].Enabled)
}

// TestInterceptWorker_DeleteRemovesWorker pins the delete dispatch path.
func TestInterceptWorker_DeleteRemovesWorker(t *testing.T) {
	crud := &fakeCRUD{graph: map[string]workers.Worker{"alpha": crudSampleWorkerForTest("alpha")}}
	deps := workerTestDeps{crud: crud}
	handled, body, isErr := callWorker(t, deps, `{"operation":"delete","name":"alpha"}`)
	require.True(t, handled)
	require.False(t, isErr, "expected non-error, got: %s", body)
	assert.Contains(t, body, "deleted")
	assert.Equal(t, []string{"alpha"}, crud.deletes)
}

// TestInterceptWorker_DeleteNotFound pins the not-found error message
// emitted when the wire layer maps kggraphclient.ErrNotFound back to a clean
// per-op error.
func TestInterceptWorker_DeleteNotFound(t *testing.T) {
	crud := &fakeCRUD{deleteErr: graphclient.ErrNotFound}
	deps := workerTestDeps{crud: crud}
	handled, body, isErr := callWorker(t, deps, `{"operation":"delete","name":"ghost"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "ghost")
	assert.Contains(t, body, "not found")
}

// TestInterceptWorker_NilCRUDIsFriendlyError pins that every CRUD op
// nil-checks the WorkerCRUD accessor and emits a degraded-boot error
// (matches the parallel nil-runtime tests for trigger/status).
func TestInterceptWorker_NilCRUDIsFriendlyError(t *testing.T) {
	deps := workerTestDeps{crud: nil}
	for _, op := range []string{"list", "create", "update", "delete"} {
		args := `{"operation":"` + op + `","name":"alpha"}`
		handled, body, isErr := callWorker(t, deps, args)
		require.True(t, handled, "op=%s must be handled even with nil crud", op)
		require.True(t, isErr, "op=%s must surface degraded-boot error", op)
		assert.Contains(t, body, "workerCRUD not wired")
	}
}

// crudSampleWorkerForTest is a minimal worker that satisfies Worker.Validate
// so the list/update tests have a non-zero fixture without dragging in
// the workercrud package fixtures.
func crudSampleWorkerForTest(name string) workers.Worker {
	return workers.Worker{
		Name:                name,
		SystemPrompt:        "you are a test worker",
		Provider:            "anthropic",
		Model:               "claude-haiku-4-5",
		ToolAllowlist:       []string{"search"},
		MaxIterations:       4,
		MaxWallclockSeconds: 30,
		Enabled:             true,
	}
}

// TestInterceptWorker_StatusWorkerNotFound pins the pre-flight existence
// check added during the OSS-readiness sweep: a misspelled / non-existent
// worker name must surface as "not found" instead of the silent "null"
// output ReadRecent produces when no log file exists. Hint to the user
// references the list operation so they can discover valid names.
func TestInterceptWorker_StatusWorkerNotFound(t *testing.T) {
	rt := &fakeRuntime{byNameNotFound: true}
	deps := workerTestDeps{runtime: rt}
	handled, body, isErr := callWorker(t, deps, `{"operation":"status","name":"phantom"}`)
	require.True(t, handled)
	require.True(t, isErr, "missing worker must surface as error, not silent null")
	assert.Contains(t, body, "phantom")
	assert.Contains(t, body, "not found")
	assert.Contains(t, body, "list", "hint must point at the list operation")
	assert.Empty(t, rt.statusCalls, "Status must not be called when worker not found")
}
