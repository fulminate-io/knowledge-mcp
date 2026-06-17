// SPDX-License-Identifier: Apache-2.0

// graphtype_crud_test.go — covers InterceptGraphType dispatch + the
// register/update/delete/list handlers. Mirrors worker_crud_test.go.

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeGraphTypeCRUD satisfies GraphTypeCRUDAPI without a real wire-loopback
// client. It records every mutation so tests can pin per-op behavior.
type fakeGraphTypeCRUD struct {
	mu        sync.Mutex
	graph     map[string]*knowledgev1.GraphTypeDef
	creates   []*knowledgev1.GraphTypeDef
	updates   []*knowledgev1.GraphTypeDef
	deletes   []string
	listErr   error
	createErr error
	updateErr error
	deleteErr error
}

func (f *fakeGraphTypeCRUD) List(_ context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*knowledgev1.GraphTypeDef, 0, len(f.graph))
	for _, d := range f.graph {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeGraphTypeCRUD) ByName(_ context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.graph[name]
	return d, ok, nil
}

func (f *fakeGraphTypeCRUD) Create(_ context.Context, d *knowledgev1.GraphTypeDef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.creates = append(f.creates, d)
	return nil
}

func (f *fakeGraphTypeCRUD) Update(_ context.Context, d *knowledgev1.GraphTypeDef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updates = append(f.updates, d)
	return nil
}

func (f *fakeGraphTypeCRUD) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, name)
	return nil
}

// graphTypeTestDeps satisfies ClientDeps with only GraphTypeCRUD() wired.
type graphTypeTestDeps struct {
	crud GraphTypeCRUDAPI
}

func (d graphTypeTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d graphTypeTestDeps) Sink() collector.Sink                         { return nil }
func (d graphTypeTestDeps) RootDir() string                              { return "" }
func (d graphTypeTestDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d graphTypeTestDeps) WorkerReady() bool                            { return true }
func (d graphTypeTestDeps) PropReady() bool                              { return true }
func (d graphTypeTestDeps) PipelineReady() bool                          { return true }
func (d graphTypeTestDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d graphTypeTestDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d graphTypeTestDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d graphTypeTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d graphTypeTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d graphTypeTestDeps) BackendResolver() BackendResolver             { return nil }
func (d graphTypeTestDeps) GraphCaller() GraphCaller                     { return nil }
func (d graphTypeTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d graphTypeTestDeps) RepoResolver() *RepoResolver                  { return nil }
func (d graphTypeTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d graphTypeTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d graphTypeTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d graphTypeTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d graphTypeTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d graphTypeTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d graphTypeTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d graphTypeTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }

func callGraphType(t *testing.T, deps ClientDeps, argsJSON string) (handled bool, body string, isErr bool) {
	t.Helper()
	params := kgtools.CallToolParams{Name: "custom_collector", Arguments: json.RawMessage(argsJSON)}
	h, res := InterceptGraphType(deps, params)
	if !h {
		return false, "", false
	}
	require.NotEmpty(t, res.Content, "intercept handled but returned no content")
	return true, res.Content[0].Text, res.IsError
}

// TestInterceptGraphType_NameFiltering pins that InterceptGraphType returns
// (false, zero) for any tool other than "custom_collector" — including the old
// "graph_type" wire name, which is no longer recognized after the rename.
func TestInterceptGraphType_NameFiltering(t *testing.T) {
	deps := graphTypeTestDeps{crud: &fakeGraphTypeCRUD{}}
	for _, name := range []string{"graph_type", "worker", "ast", "collect", "manage", "search", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptGraphType(deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptGraphType", name)
		assert.Empty(t, res.Content, "non-custom_collector call must return zero ToolResult")
	}
}

// TestInterceptGraphType_Register routes register to Create with the parsed
// record.
func TestInterceptGraphType_Register(t *testing.T) {
	crud := &fakeGraphTypeCRUD{}
	deps := graphTypeTestDeps{crud: crud}
	handled, body, isErr := callGraphType(t, deps,
		`{"operation":"register","name":"jira","collector":{"binary_path":"/usr/local/bin/jira","param_transport":"stdin"}}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	require.Len(t, crud.creates, 1)
	assert.Equal(t, "jira", crud.creates[0].GetName())
	assert.Equal(t, "/usr/local/bin/jira", crud.creates[0].GetCollector().GetBinaryPath())
}

// TestInterceptGraphType_Update routes update to Update.
func TestInterceptGraphType_Update(t *testing.T) {
	crud := &fakeGraphTypeCRUD{}
	deps := graphTypeTestDeps{crud: crud}
	handled, body, isErr := callGraphType(t, deps,
		`{"operation":"update","name":"jira","collector":{"binary_path":"/usr/local/bin/jira","param_transport":"stdin"}}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	require.Len(t, crud.updates, 1)
	assert.Equal(t, "jira", crud.updates[0].GetName())
}

// TestInterceptGraphType_Delete routes delete to Delete.
func TestInterceptGraphType_Delete(t *testing.T) {
	crud := &fakeGraphTypeCRUD{}
	deps := graphTypeTestDeps{crud: crud}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"delete","name":"jira"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Equal(t, []string{"jira"}, crud.deletes)
}

// TestInterceptGraphType_ListSurfacesCollectorAndBehavior pins that list output
// surfaces each registered type's name + collector + behavior.
func TestInterceptGraphType_ListSurfacesCollectorAndBehavior(t *testing.T) {
	tru := true
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		"jira": {
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				BinaryPath:     "/usr/local/bin/jira",
				ParamTransport: "stdin",
			},
			Behavior: &knowledgev1.BehaviorDefaults{Syncable: &tru},
		},
	}}
	deps := graphTypeTestDeps{crud: crud}

	handled, body, isErr := callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "jira")
	assert.Contains(t, body, "/usr/local/bin/jira")
	assert.Contains(t, body, "stdin")
	assert.Contains(t, body, "true", "syncable behavior flag must surface")
}

// TestInterceptGraphType_ListEmpty pins the empty-catalog message.
func TestInterceptGraphType_ListEmpty(t *testing.T) {
	deps := graphTypeTestDeps{crud: &fakeGraphTypeCRUD{}}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "No graph types registered")
}

// TestInterceptGraphType_UnknownOperation surfaces a clear error.
func TestInterceptGraphType_UnknownOperation(t *testing.T) {
	deps := graphTypeTestDeps{crud: &fakeGraphTypeCRUD{}}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"frobnicate"}`)
	require.True(t, handled)
	assert.True(t, isErr)
	assert.Contains(t, body, "unknown operation")
}

// TestInterceptGraphType_ListJSON pins the json format path.
func TestInterceptGraphType_ListJSON(t *testing.T) {
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		"jira": {Name: "jira", Collector: &knowledgev1.CollectorSpec{BinaryPath: "/usr/local/bin/jira", ParamTransport: "stdin"}},
	}}
	deps := graphTypeTestDeps{crud: crud}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"list","format":"json"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "jira")
	assert.Contains(t, body, "binary_path")
}
