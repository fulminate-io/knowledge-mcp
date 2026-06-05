// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeBackend is a scripted backends.Backend implementation used by
// intercept tests. Records calls; returns scripted refs and errors.
type fakeBackend struct {
	name string

	groupsResult []backends.Group
	groupsErr    error

	createProjectRef backends.RemoteRef
	createProjectErr error
	createProjectArg backends.ProjectCreateArgs

	createTicketRef backends.RemoteRef
	createTicketErr error
	createTicketArg backends.TicketCreateArgs

	updateProjectErr  error
	updateTicketErr   error
	archiveProjectErr error
	archiveTicketErr  error

	updateProjectCalls  int
	updateTicketCalls   int
	archiveProjectCalls int
	archiveTicketCalls  int
}

func (f *fakeBackend) Name() string {
	if f.name == "" {
		return "linear"
	}
	return f.name
}
func (f *fakeBackend) Groups(_ context.Context) ([]backends.Group, error) {
	return f.groupsResult, f.groupsErr
}
func (f *fakeBackend) SyncGroup(_ context.Context, _ string) (backends.Snapshot, error) {
	return backends.Snapshot{}, nil
}
func (f *fakeBackend) CreateProject(_ context.Context, a backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	f.createProjectArg = a
	return f.createProjectRef, f.createProjectErr
}
func (f *fakeBackend) UpdateProject(_ context.Context, _ backends.RemoteRef, _ backends.ProjectDiff) error {
	f.updateProjectCalls++
	return f.updateProjectErr
}
func (f *fakeBackend) ArchiveProject(_ context.Context, _ backends.RemoteRef) error {
	f.archiveProjectCalls++
	return f.archiveProjectErr
}
func (f *fakeBackend) CreateTicket(_ context.Context, a backends.TicketCreateArgs) (backends.RemoteRef, error) {
	f.createTicketArg = a
	return f.createTicketRef, f.createTicketErr
}
func (f *fakeBackend) UpdateTicket(_ context.Context, _ backends.RemoteRef, _ backends.TicketDiff) error {
	f.updateTicketCalls++
	return f.updateTicketErr
}
func (f *fakeBackend) ArchiveTicket(_ context.Context, _ backends.RemoteRef) error {
	f.archiveTicketCalls++
	return f.archiveTicketErr
}

// fakeResolver wires a single backend; nil means no backend.
type fakeResolver struct {
	def    backends.Backend
	byName map[string]backends.Backend
}

func (f fakeResolver) Default() backends.Backend { return f.def }
func (f fakeResolver) ByName(name string) backends.Backend {
	if b, ok := f.byName[name]; ok {
		return b
	}
	return nil
}

// interceptTestDeps satisfies ClientDeps for intercept tests. Wires
// only the BackendResolver + GraphCaller (the only two the intercepts
// touch). Everything else is nil.
type interceptTestDeps struct {
	backend backends.Backend
	byName  map[string]backends.Backend
	gc      GraphCaller
}

func (d interceptTestDeps) GraphClient() *graphclient.GraphClient { return nil }
func (d interceptTestDeps) Sink() collector.Sink                  { return nil }
func (d interceptTestDeps) RootDir() string                       { return "" }
func (d interceptTestDeps) WorkerRuntime() WorkerRuntimeAPI       { return nil }
func (d interceptTestDeps) WorkerCRUD() WorkerCRUDAPI             { return nil }
func (d interceptTestDeps) Embedder() embed.BinaryEmbedder        { return nil }
func (d interceptTestDeps) BackendResolver() BackendResolver {
	return fakeResolver{def: d.backend, byName: d.byName}
}
func (d interceptTestDeps) GraphCaller() GraphCaller         { return d.gc }
func (d interceptTestDeps) LocalGraphCaller() GraphCaller    { return d.gc }
func (d interceptTestDeps) RepoResolver() *RepoResolver      { return nil }
func (d interceptTestDeps) SegmentManager() SegmentSearcher  { return nil }
func (d interceptTestDeps) SegmentShipper() SegmentShipper   { return nil }
func (d interceptTestDeps) PipelineScanner() PipelineScanner { return nil }

func TestInterceptCreateProject_NoBackend_ClaimsLocalOnly(t *testing.T) {
	// Phase 3a: no-backend path is now claimed client-side.
	// The server has no create_project handler, so this intercept
	// MUST claim the call to produce a real response.
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-x"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s"}`),
	})
	assert.True(t, handled, "no-backend path is now claimed client-side")
	assert.False(t, res.IsError, "local-only create must succeed: %s", toolResultText(res))
}

func TestInterceptCreateProject_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{backend: &fakeBackend{}, gc: &fakeGraphCaller{}}
	handled, _ := InterceptCreateProject(deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

func TestInterceptCreateProject_LinearError_ReturnsErrorResult(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-1"}},
		createProjectErr: errors.New("linear: 401 unauthorized"),
	}
	deps := interceptTestDeps{backend: fb, gc: &fakeGraphCaller{}}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "401 unauthorized")
}

func TestInterceptCreateProject_Success_StampsBackendMetadata(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	// The client no longer forwards create_project — instead
	// it issues mutate(create_batch). The fake's mutateResult feeds the
	// create_batch RPC. Returned ids are read off the JSON.
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "success should not be an error result: %s", toolResultText(res))
	// One CREATE Mutation Execute (carrier path) with backend metadata stamped on
	// the project NodeBody (the create rides a MutationPlan now,
	// not the formatted create_batch wire envelope).
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	md := m.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, "linear", md["backend"])
	assert.Equal(t, "proj-uuid", md["linear_id"])
	assert.Equal(t, "https://example.invalid/p", md["external_url"])
	assert.Equal(t, "team-uuid", md["linear_group_id"])
	assert.Equal(t, "FUL", md["linear_group_key"])
}

func TestInterceptCreateProject_GroupNotFound_Errors(t *testing.T) {
	fb := &fakeBackend{
		groupsResult: []backends.Group{{Key: "FUL", ID: "team-1"}},
	}
	deps := interceptTestDeps{backend: fb, gc: &fakeGraphCaller{}}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"WRONG"}`),
	})
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "WRONG")
	assert.Contains(t, toolResultText(res), "FUL")
}

func TestInterceptCreateProject_AutoDefaultsSingleGroup(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-x"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError)
	assert.Equal(t, "FUL", fb.createProjectArg.GroupKey, "single-group auto-default")
}

func TestInterceptCreateProject_ForwardError_NamesLinearID(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	// PersistBatch failure → surfaced as "local mirror failed" with
	// Linear identifiers so the operator can reconcile.
	fc := &fakeGraphCaller{mutateError: errors.New("connect: refused")}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "Linear create succeeded")
	assert.Contains(t, body, "proj-uuid")
	assert.Contains(t, body, "https://example.invalid/p")
	assert.Contains(t, body, "local mirror failed")
}
