// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// syncableBehavior builds a BehaviorDefaults whose syncable flag is v.
func syncableBehavior(v bool) *knowledgev1.BehaviorDefaults {
	return &knowledgev1.BehaviorDefaults{Syncable: &v}
}

// syncableCRUD builds a fakeGraphTypeCRUD seeded with one syncable=true and one
// syncable=false custom GraphTypeDef.
func syncableCRUD() *fakeGraphTypeCRUD {
	return &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		"syncgraph":   {Name: "syncgraph", Behavior: syncableBehavior(true)},
		"nosyncgraph": {Name: "nosyncgraph", Behavior: syncableBehavior(false)},
	}}
}

// --- list: syncable:true appears, syncable:false excluded, builtins unchanged ---

// listEnumCaller is a GraphCaller whose Execute (driven by fetchGraphNamesOfType's
// query(mode:modules)) returns one GraphInfo named "<graph>-inst" for the graph
// named in the request target, recording which graph types were enumerated.
type listEnumCaller struct {
	enumerated []string
}

func (c *listEnumCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	graph := req.GetTarget().GetGraph()
	c.enumerated = append(c.enumerated, graph)
	return &knowledgev1.ExecuteResponse{
		GraphNames: []*knowledgev1.GraphInfo{{Name: graph + "-inst"}},
	}, nil
}

// listDeps wires LocalGraphCaller + GraphTypeCRUD for the sync list path (not
// logged in, so no cloud enumeration).
type listDeps struct {
	local GraphCaller
	crud  GraphTypeCRUDAPI
}

func (d listDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d listDeps) Sink() collector.Sink                         { return nil }
func (d listDeps) RootDir() string                              { return "" }
func (d listDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d listDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d listDeps) WorkerReady() bool                            { return true }
func (d listDeps) PropReady() bool                              { return true }
func (d listDeps) PipelineReady() bool                          { return true }
func (d listDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d listDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d listDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d listDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d listDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d listDeps) BackendResolver() BackendResolver             { return nil }
func (d listDeps) GraphCaller() GraphCaller                     { return nil }
func (d listDeps) LocalGraphCaller() GraphCaller                { return d.local }
func (d listDeps) SegmentManager() SegmentSearcher              { return nil }
func (d listDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d listDeps) SegmentShipper() SegmentShipper               { return nil }
func (d listDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d listDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d listDeps) PipelineScanner() PipelineScanner             { return nil }
func (d listDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d listDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d listDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d listDeps) ClusterProvider() ClusterProvider     { return nil }
func (d listDeps) TensionsProvider() TensionsProvider   { return nil }

// TestSyncList_SyncableCustomTypes is the Group C list guard: the syncable:true
// custom type's instances appear in the rendered table; the syncable:false custom
// type does NOT (it is never enumerated); builtin sync-eligible types still appear.
// Reverting the syncableCustomTypes widening makes the syncgraph assertion FAIL.
func TestSyncList_SyncableCustomTypes(t *testing.T) {
	enum := &listEnumCaller{}
	res := handleSyncList(listDeps{local: enum, crud: syncableCRUD()})
	require.False(t, res.IsError, "list must not error: %v", res.Content)
	text := res.Content[0].Text

	assert.Contains(t, text, "syncgraph/syncgraph-inst", "the syncable:true custom type's instance appears")
	assert.NotContains(t, text, "nosyncgraph", "the syncable:false custom type is excluded from the table")
	assert.NotContains(t, enum.enumerated, "nosyncgraph", "the syncable:false custom type is never enumerated")

	// Builtins unchanged: every sync-eligible builtin was still enumerated.
	for _, gt := range kgtypes.SyncEligibleGraphTypes() {
		assert.Contains(t, enum.enumerated, string(gt), "builtin sync-eligible type %q still enumerated", gt)
	}
}

// --- push: syncable gate before any ExportGraph RPC ---

// TestInterceptSync_Push_SyncableGate covers the push syncable gate: a custom
// graph with syncable=true reaches the Exporter; syncable=false and unregistered
// are rejected BEFORE any ExportGraph call. Reverting the gate makes the rejection
// assertions FAIL (the export would fire for a non-syncable custom type).
func TestInterceptSync_Push_SyncableGate(t *testing.T) {
	okTransport := func(t *testing.T, backend *fakeSyncBackend) {
		withTransport(t, func() (*auth.Transport, error) {
			src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
			return auth.NewSyncTransport(backend.srv.URL, src), nil
		})
	}

	t.Run("syncable:true custom reaches ExportGraph", func(t *testing.T) {
		backend := newFakeSyncBackend(t)
		okTransport(t, backend)
		exp := &fakeExporter{bytesOut: []byte("KGV4xx")}
		_, out := InterceptSync(interceptTestDeps{gc: exp, crud: syncableCRUD()},
			syncParams(t, map[string]any{"operation": "push", "graph": "syncgraph", "name": "demo"}))
		assert.False(t, out.IsError, "syncable:true custom push must proceed: %q", textOf(out))
		assert.Equal(t, 1, exp.exportCalls, "ExportGraph fired for the syncable custom type")
		assert.Equal(t, 1, backend.confirmCalls, "the push completed through confirm")
	})

	t.Run("syncable:false custom is rejected before ExportGraph", func(t *testing.T) {
		// The syncable gate rejects BEFORE any transport build, so no backend is
		// needed; guard that the transport is never built.
		withTransport(t, func() (*auth.Transport, error) {
			t.Fatal("transport must not be built for a non-syncable push")
			return nil, nil
		})
		exp := &fakeExporter{bytesOut: []byte{1, 2}}
		_, out := InterceptSync(interceptTestDeps{gc: exp, crud: syncableCRUD()},
			syncParams(t, map[string]any{"operation": "push", "graph": "nosyncgraph", "name": "demo"}))
		assert.True(t, out.IsError, "syncable:false custom push must be rejected")
		assert.Equal(t, 0, exp.exportCalls, "no ExportGraph call for a non-syncable type")
	})

	t.Run("unregistered custom is rejected before ExportGraph", func(t *testing.T) {
		withTransport(t, func() (*auth.Transport, error) {
			t.Fatal("transport must not be built for an unregistered push")
			return nil, nil
		})
		exp := &fakeExporter{bytesOut: []byte{1, 2}}
		_, out := InterceptSync(interceptTestDeps{gc: exp, crud: syncableCRUD()},
			syncParams(t, map[string]any{"operation": "push", "graph": "ghostgraph", "name": "demo"}))
		assert.True(t, out.IsError, "unregistered custom push must be rejected")
		assert.Equal(t, 0, exp.exportCalls, "no ExportGraph call for an unregistered type")
	})
}

// --- pull: syncable gate before any ExportGraph/OverwriteGraph RPC ---

// TestInterceptSync_Pull_SyncableGate covers the pull syncable gate symmetrically:
// syncable=true reaches ExportGraph + OverwriteGraph; syncable=false / unregistered
// are rejected before any RPC.
func TestInterceptSync_Pull_SyncableGate(t *testing.T) {
	t.Run("syncable:true custom reaches pull + OverwriteGraph", func(t *testing.T) {
		want := []byte("KGV4 syncable custom graph")
		backend := newFakeSyncBackend(t)
		backend.pullPlaintext = want
		withTransport(t, func() (*auth.Transport, error) {
			src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
			return auth.NewSyncTransport(backend.srv.URL, src), nil
		})
		local := &fakeOverwriter{nodes: 1}
		_, out := InterceptSync(pullDeps{local: local, crud: syncableCRUD()},
			syncParams(t, map[string]any{"operation": "pull", "graph": "syncgraph", "name": "demo"}))
		assert.False(t, out.IsError, "syncable:true custom pull must proceed: %q", textOf(out))
		assert.Equal(t, 1, backend.pullCalls, "pull control endpoint fired")
		assert.Equal(t, 1, local.overwriteCalls, "local OverwriteGraph applied")
	})

	t.Run("syncable:false custom is rejected before any RPC", func(t *testing.T) {
		withTransport(t, func() (*auth.Transport, error) {
			t.Fatal("transport must not be built for a non-syncable pull")
			return nil, nil
		})
		local := &fakeOverwriter{}
		_, out := InterceptSync(pullDeps{local: local, crud: syncableCRUD()},
			syncParams(t, map[string]any{"operation": "pull", "graph": "nosyncgraph", "name": "demo"}))
		assert.True(t, out.IsError, "syncable:false custom pull must be rejected")
		assert.Equal(t, 0, local.overwriteCalls, "no local OverwriteGraph for a non-syncable type")
	})
}
