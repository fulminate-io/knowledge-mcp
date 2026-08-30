// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// logE2EDeps is a minimal ClientDeps that exposes a GraphCaller. Other
// accessors return nil — they're not exercised by the log intercepts.
// Mirrors fakeDeps in collect_logs_e2e_test.go (per the plan reuse
// target) but with an explicit graph-caller-only surface.
type logE2EDeps struct {
	gc GraphCaller
}

func (d *logE2EDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *logE2EDeps) Sink() collector.Sink            { return nil }
func (d *logE2EDeps) RootDir() string                 { return "" }
func (d *logE2EDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *logE2EDeps) PropReady() bool     { return true }
func (d *logE2EDeps) PipelineReady() bool { return true }

func (d *logE2EDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *logE2EDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *logE2EDeps) BackendResolver() BackendResolver             { return nil }
func (d *logE2EDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *logE2EDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *logE2EDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *logE2EDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *logE2EDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *logE2EDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *logE2EDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *logE2EDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *logE2EDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *logE2EDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *logE2EDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *logE2EDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *logE2EDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *logE2EDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *logE2EDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *logE2EDeps) TensionsProvider() TensionsProvider   { return nil }

// e2eSetupLogGraph seeds a small store-FREE log graph (templates + chunk +
// stream + a correlation edge) onto a fakeLogGraphCaller. Returns the queryID
// and a Deps that routes through the fake — the intercept chain reads the same
// node + edge data over the Execute carrier seam it would have read from a
// local store before this migration.
func e2eSetupLogGraph(t *testing.T) (string, ClientDeps) {
	t.Helper()
	queryID := "q-e2e-logs"

	nodes := []*knowledgev1.Node{
		{Id: "tpl-a", Type: string(kgtypes.NodeLogTemplate), SymbolName: "tpl a",
			Metadata: map[string]string{"pattern": "tpl a", "severity": "ERROR", "count": "5"}},
		{Id: "tpl-b", Type: string(kgtypes.NodeLogTemplate), SymbolName: "tpl b",
			Metadata: map[string]string{"pattern": "tpl b", "severity": "WARN", "count": "2"}},
		{Id: "chunk-1", Type: string(kgtypes.NodeLogChunk), SymbolName: "chunk 1",
			Metadata: map[string]string{"template_id": "tpl-a", "stream_id": "stream-1", "entry_count": "3"}},
		{Id: "stream-1", Type: string(kgtypes.NodeLogStream), SymbolName: "api stream",
			Metadata: map[string]string{"label:service": "api"}},
	}
	edges := []*knowledgev1.Edge{
		{FromId: "tpl-a", ToId: "tpl-b", Type: string(kgtypes.EdgeCorrelatesWith), Confidence: 0.9, Method: "test", Evidence: "services=api,db"},
		{FromId: "tpl-a", ToId: "chunk-1", Type: string(kgtypes.EdgeContains)},
	}
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	return queryID, &logE2EDeps{gc: fake}
}

// TestE2E_QueryGraphLogs_Correlations exercises InterceptLogsQuery
// end-to-end on mode:correlations against a seeded log graph.
func TestE2E_QueryGraphLogs_Correlations(t *testing.T) {
	queryID, deps := e2eSetupLogGraph(t)

	args, err := json.Marshal(map[string]any{
		"graph": "logs", "name": queryID, "mode": "correlations",
	})
	require.NoError(t, err)
	handled, res := InterceptLogsQuery(opCtx(), deps, kgtools.CallToolParams{
		Name: "query", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsQuery must claim graph=logs")
	require.False(t, res.IsError, "correlations rendering must succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Log correlations")
	assert.Contains(t, body, queryID)
}

// TestE2E_TraverseGraphLogs_Template exercises InterceptLogsTraversal
// end-to-end on a per-template walk against the seeded log graph.
func TestE2E_TraverseGraphLogs_Template(t *testing.T) {
	queryID, deps := e2eSetupLogGraph(t)

	args, err := json.Marshal(map[string]any{
		"graph": "logs", "name": queryID, "start": "tpl-a", "direction": "out",
	})
	require.NoError(t, err)
	handled, res := InterceptLogsTraversal(opCtx(), deps, kgtools.CallToolParams{
		Name: "traverse", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsTraversal must claim graph=logs+start")
	require.False(t, res.IsError, "template traversal must succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Log template tpl-a")
}

// TestE2E_ManageListLogs exercises InterceptLogsManage on list_logs.
// Verifies the full chain: intercept → handleListLogs → fetchGraphNamesOfType
// → fakeLogGraphCaller.execGraphNames (RETURN_MODE_GRAPH_NAMES).
func TestE2E_ManageListLogs(t *testing.T) {
	queryID, deps := e2eSetupLogGraph(t)

	args, err := json.Marshal(map[string]any{
		"operation": "list_logs", "format": "json",
	})
	require.NoError(t, err)
	handled, res := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsManage must claim list_logs")
	require.False(t, res.IsError, "list_logs must succeed: %s", toolResultText(res))
	var summaries []logGraphSummary
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &summaries))
	require.Len(t, summaries, 1)
	assert.Equal(t, queryID, summaries[0].QueryID)
}

// TestE2E_ManageDiscardLogs exercises InterceptLogsManage on
// discard_logs by name.
func TestE2E_ManageDiscardLogs(t *testing.T) {
	queryID, deps := e2eSetupLogGraph(t)

	args, err := json.Marshal(map[string]any{
		"operation": "discard_logs", "name": queryID,
	})
	require.NoError(t, err)
	handled, res := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsManage must claim discard_logs")
	require.False(t, res.IsError, "discard_logs must succeed: %s", toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, "Discarded 1 log graph")
	assert.Contains(t, body, queryID)

	// After discard, a follow-up list_logs must report no graphs — the
	// DROP_GRAPH removed it from the fake's catalog.
	listArgs, err := json.Marshal(map[string]any{"operation": "list_logs"})
	require.NoError(t, err)
	handled, listRes := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: listArgs,
	})
	require.True(t, handled)
	require.False(t, listRes.IsError, "post-discard list_logs must succeed: %s", toolResultText(listRes))
	assert.Contains(t, toolResultText(listRes), "No active log graphs",
		"log graph must be absent from the catalog after discard")
}

// TestE2E_ClientSide_ConfigureLogBackend asserts that the configure
// intercept arm runs the moved client-side handler, which issues a
// query("log-backend") (no-existing-record probe) followed by a
// mutate(upsert, type:"log-backend") against the server. The fake store
// behaves like an empty backend list for the pre-flight query so the
// handler takes the create-path.
func TestE2E_ClientSide_ConfigureLogBackend(t *testing.T) {
	gc := newFakeBackendStore()
	deps := &logE2EDeps{gc: gc}

	args, err := json.Marshal(map[string]any{
		"operation":    "configure_log_backend",
		"provider":     "k8s",
		"name":         "test-backend",
		"url":          "https://example.invalid",
		"auth_type":    "kubeconfig",
		"kube_context": "docker-desktop",
	})
	require.NoError(t, err)
	handled, res := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsManage must claim configure_log_backend")
	require.False(t, res.IsError, "configure_log_backend must succeed: %s", toolResultText(res))
	// Wire shape: one query (pre-flight lookup) + one mutate (upsert).
	require.Len(t, gc.calls, 2, "expected query + mutate RPCs, got: %d", len(gc.calls))
	assert.Equal(t, "query", gc.calls[0].tool)
	assert.Equal(t, "mutate", gc.calls[1].tool)
}

// TestE2E_ClientSide_ListLogBackends asserts that the list intercept arm
// runs the moved client-side handler, which issues a query("log-backend")
// against the server's generic query handler.
func TestE2E_ClientSide_ListLogBackends(t *testing.T) {
	gc := newFakeBackendStore()
	deps := &logE2EDeps{gc: gc}

	args, err := json.Marshal(map[string]any{
		"operation": "list_log_backends",
	})
	require.NoError(t, err)
	handled, res := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: args,
	})
	require.True(t, handled, "InterceptLogsManage must claim list_log_backends")
	require.False(t, res.IsError)
	require.Len(t, gc.calls, 1, "expected one query RPC")
	assert.Equal(t, "query", gc.calls[0].tool)
}

// TestE2E_NonLogsCalls_FallThrough asserts the three log intercepts
// return (false, _) for non-log calls so the chain continues.
func TestE2E_NonLogsCalls_FallThrough(t *testing.T) {
	deps := &logE2EDeps{}

	t.Run("query non-logs", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"graph": "knowledge"})
		handled, _ := InterceptLogsQuery(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		assert.False(t, handled)
	})
	t.Run("traverse non-logs", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"graph": "code", "start": "x"})
		handled, _ := InterceptLogsTraversal(opCtx(), deps, kgtools.CallToolParams{Name: "traverse", Arguments: args})
		assert.False(t, handled)
	})
	t.Run("manage non-logs", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"operation": "status"})
		handled, _ := InterceptLogsManage(opCtx(), deps, kgtools.CallToolParams{Name: "manage", Arguments: args})
		assert.False(t, handled)
	})
	t.Run("traverse logs no start", func(t *testing.T) {
		// Graph-wide enumeration → server-side, intercept must fall through.
		args, _ := json.Marshal(map[string]any{"graph": "logs", "name": "x"})
		handled, _ := InterceptLogsTraversal(opCtx(), deps, kgtools.CallToolParams{Name: "traverse", Arguments: args})
		assert.False(t, handled, "graph-wide traverse should fall through to the server")
	})
}
