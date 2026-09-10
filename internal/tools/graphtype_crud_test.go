// SPDX-License-Identifier: Apache-2.0

// graphtype_crud_test.go — covers InterceptGraphType dispatch + the one
// operation that survives the config-file contract, list.
//
// THE WRITE-OPERATION ROWS WENT WITH THE OPERATIONS. register, update and delete
// wrote a record the config file now owns; what replaced them is
// `knowledge collector add | remove`, whose rows live in the bootstrap package,
// and a REFUSAL row below asserting that the retired names are refused by name
// rather than quietly accepted.

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphTypeCRUD satisfies GraphTypeCRUDAPI without a real wire-loopback
// client. It records every mutation so tests can pin per-op behavior.
type fakeGraphTypeCRUD struct {
	mu        sync.Mutex
	graph     map[string]*knowledgev1.GraphTypeDef
	updates   []*knowledgev1.GraphTypeDef
	listErr   error
	updateErr error
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

func (f *fakeGraphTypeCRUD) Update(_ context.Context, d *knowledgev1.GraphTypeDef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updates = append(f.updates, d)
	return nil
}

// graphTypeTestDeps satisfies ClientDeps with only GraphTypeCRUD() wired.
type graphTypeTestDeps struct {
	crud GraphTypeCRUDAPI
}

func (d graphTypeTestDeps) LocalLiveness() LocalLiveness    { return nil }
func (d graphTypeTestDeps) Sink() collector.Sink            { return nil }
func (d graphTypeTestDeps) RootDir() string                 { return "" }
func (d graphTypeTestDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d graphTypeTestDeps) PropReady() bool     { return true }
func (d graphTypeTestDeps) PipelineReady() bool { return true }

func (d graphTypeTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d graphTypeTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d graphTypeTestDeps) BackendResolver() BackendResolver             { return nil }
func (d graphTypeTestDeps) GraphCaller() GraphCaller                     { return nil }
func (d graphTypeTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d graphTypeTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d graphTypeTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d graphTypeTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d graphTypeTestDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d graphTypeTestDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d graphTypeTestDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d graphTypeTestDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d graphTypeTestDeps) PipelineScanner() PipelineScanner         { return nil }

func (d graphTypeTestDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d graphTypeTestDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d graphTypeTestDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d graphTypeTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d graphTypeTestDeps) ClusterProvider() ClusterProvider     { return nil }
func (d graphTypeTestDeps) TensionsProvider() TensionsProvider   { return nil }

func callGraphType(t *testing.T, deps ClientDeps, argsJSON string) (handled bool, body string, isErr bool) {
	t.Helper()
	params := kgtools.CallToolParams{Name: "custom_collector", Arguments: json.RawMessage(argsJSON)}
	h, res := InterceptGraphType(opCtx(), deps, params)
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
	for _, name := range []string{"graph_type", "ast", "collect", "manage", "search", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptGraphType(opCtx(), deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptGraphType", name)
		assert.Empty(t, res.Content, "non-custom_collector call must return zero ToolResult")
	}
}

// TestInterceptGraphType_RetiredWriteOperationsAreRefusedByName pins that the
// three retired operations are REFUSED, naming what is admitted, rather than
// falling into a default arm that reports success for a write nothing performed.
//
// THE ADMITTED SET IS READ OFF THE LIVE TOOL DEFINITION, not a hand-written
// list: a schema still advertising `register` while the dispatch refuses it
// would be a tool whose own description lies to its caller, and only reading
// both in one test catches that.
func TestInterceptGraphType_RetiredWriteOperationsAreRefusedByName(t *testing.T) {
	crud := &fakeGraphTypeCRUD{}
	deps := graphTypeTestDeps{crud: crud}
	for _, op := range []string{"register", "update", "delete"} {
		handled, body, isErr := callGraphType(t, deps, `{"operation":"`+op+`"}`)
		require.True(t, handled)
		assert.True(t, isErr, "the retired %s operation must be refused: %s", op, body)
		assert.Contains(t, body, op, "the refusal must name what was asked for")
		assert.Contains(t, body, "list", "and what is admitted instead")

		// THE RETIRED ARGUMENTS GO WITH THE OPERATIONS: a call still carrying the
		// record fields is refused by the top-level parameter accounting, naming the
		// key, rather than being decoded and ignored.
		_, body, isErr = callGraphType(t, deps, `{"operation":"`+op+`","name":"jira","collector":{"http":{"url":"https://p.example/mcp"}}}`)
		assert.True(t, isErr, "a retired argument must be refused: %s", body)
		assert.Contains(t, body, "unknown parameter", "the refusal must say what it did not accept")
	}
	assert.Empty(t, crud.updates, "a refused operation must write nothing")

	enum := GraphTypeToolDef().InputSchema.Properties["operation"].Enum
	assert.Equal(t, []string{"list"}, enum,
		"the advertised operation enum must match the dispatch: a schema offering an operation the dispatch refuses is a tool lying to its caller")
}

// TestInterceptGraphType_ListSurfacesCollectorAndBehavior pins that list output
// surfaces each registered type's name + collector + behavior.
func TestInterceptGraphType_ListSurfacesCollectorAndBehavior(t *testing.T) {
	tru := true
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		"jira": {
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool: "collect_jira",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/usr/local/bin/jira-mcp",
				}},
			},
			Behavior: &knowledgev1.BehaviorDefaults{Syncable: &tru},
		},
	}}
	deps := graphTypeTestDeps{crud: crud}

	handled, body, isErr := callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "jira")
	assert.Contains(t, body, "/usr/local/bin/jira-mcp")
	assert.Contains(t, body, "collect_jira")
	assert.Contains(t, body, "true", "syncable behavior flag must surface")
}

// TestInterceptGraphType_ListEmpty pins the empty-catalog message.
func TestInterceptGraphType_ListEmpty(t *testing.T) {
	deps := graphTypeTestDeps{crud: &fakeGraphTypeCRUD{}}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "No custom collector families are known to the server")
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
		"jira": {Name: "jira", Collector: &knowledgev1.CollectorSpec{
			Tool:     "collect_jira",
			Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{Command: "/usr/local/bin/jira-mcp"}},
		}},
	}}
	deps := graphTypeTestDeps{crud: crud}
	handled, body, isErr := callGraphType(t, deps, `{"operation":"list","format":"json"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "jira")
	assert.Contains(t, body, "provider")
}
