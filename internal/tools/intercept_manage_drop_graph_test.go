// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dropGraphCall drives InterceptManage(drop_graph) over a fakeGraphCaller wired
// as the deps GraphCaller. The fake records every ExecuteRequest in execRequests
// (and the MutationPlan in execMutations), so the tests assert the exact
// DROP_GRAPH envelope + selector the client lowered. mutateAffected is returned
// as the Execute affected_count so the ack-count rendering is observable.
func dropGraphCall(t *testing.T, fc *fakeGraphCaller, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	deps := interceptTestDeps{gc: fc}
	return InterceptManage(deps, kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

// TestInterceptManage_DropGraph_CustomGraph asserts a drop of a registered
// custom graph records EXACTLY ONE MUTATION_KIND_DROP_GRAPH ExecuteRequest whose
// Target is the manageGraphSelector envelope (custom→Name). Reverting the handler
// to skip the Execute, or to build the wrong selector, fails this.
func TestInterceptManage_DropGraph_CustomGraph(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 12}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph","graph":"hellograph","name":"demo"}`)
	require.True(t, handled, "drop_graph must be handled by InterceptManage")
	require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

	require.Len(t, fc.execRequests, 1, "exactly one Execute RPC")
	req := fc.execRequests[0]
	require.NotNil(t, req.GetMutation(), "the Execute plan is a Mutation")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH, req.GetMutation().GetKind(),
		"the mutation kind is DROP_GRAPH")
	tgt := req.GetTarget()
	assert.Equal(t, "hellograph", tgt.GetGraph(), "Target graph is the custom type")
	assert.Equal(t, "demo", tgt.GetName(), "custom graph routes name via Name")
	assert.Empty(t, tgt.GetRepo(), "custom graph must not set Repo")

	body := toolResultText(res)
	assert.Contains(t, body, "Dropped graph hellograph/demo")
	assert.Contains(t, body, "12", "the affected_count is rendered")
}

// TestInterceptManage_DropGraph_CodeRoutesRepo asserts graph=code routes the name
// onto Repo (not Name) — proving the manageGraphSelector family routing.
func TestInterceptManage_DropGraph_CodeRoutesRepo(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 1}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph","graph":"code","name":"myrepo"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

	require.Len(t, fc.execRequests, 1)
	tgt := fc.execRequests[0].GetTarget()
	assert.Equal(t, "code", tgt.GetGraph())
	assert.Equal(t, "myrepo", tgt.GetRepo(), "code routes name via Repo")
	assert.Empty(t, tgt.GetName(), "code must not set Name")
}

// TestInterceptManage_DropGraph_EmptyGraphRejected asserts an empty graph is a
// validation error with NO Execute fired.
func TestInterceptManage_DropGraph_EmptyGraphRejected(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "empty graph must error")
	assert.Contains(t, toolResultText(res), "requires")
	assert.Empty(t, fc.execRequests, "no Execute RPC when the required graph is missing")
}

// TestInterceptManage_DropGraph_LogsRejected asserts graph=logs is rejected with
// a pointer to discard_logs (the logs-path-owner invariant) and fires no Execute.
func TestInterceptManage_DropGraph_LogsRejected(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph","graph":"logs","name":"q-123"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "graph=logs must error")
	assert.Contains(t, toolResultText(res), "discard_logs", "the error points at discard_logs")
	assert.Empty(t, fc.execRequests, "no Execute RPC for graph=logs")
}

// TestInterceptManage_DropGraph_DryRunPreviewOnly asserts dry_run:true issues
// ZERO Execute mutations and renders a "would drop" preview (the delete-tool
// dry_run verb discipline) — never "Dropped".
func TestInterceptManage_DropGraph_DryRunPreviewOnly(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph","graph":"hellograph","name":"demo","dry_run":true}`)
	require.True(t, handled)
	require.False(t, res.IsError, "dry_run preview is not an error: %s", toolResultText(res))

	assert.Empty(t, fc.execRequests, "dry_run issues ZERO Execute mutations")
	body := toolResultText(res)
	assert.Contains(t, body, "DRY RUN", "the preview is labeled DRY RUN")
	assert.Contains(t, body, "would drop graph hellograph/demo", "verb is 'would drop'")
	assert.NotContains(t, body, "Dropped graph", "dry_run must NOT claim a completed drop")
}

// TestInterceptManage_DropGraph_LogsPathRegression asserts the existing
// handleDiscardLogs path still tears down a named log graph unchanged AFTER the
// new drop_graph op lands — i.e. the new op did not perturb the logs discard
// path. Drives the existing fakeLogGraphCaller harness (setupLogTestHandler) and
// confirms the discard removes the corpus + fires its own DROP_GRAPH Execute.
func TestInterceptManage_DropGraph_LogsPathRegression(t *testing.T) {
	const qid = "q-regress"
	h := setupLogTestHandler(t, qid)
	fc, ok := h.graphCallerOverride.(*fakeLogGraphCaller)
	require.True(t, ok, "the log test handler is wired with a fakeLogGraphCaller")
	require.Contains(t, fc.graphs, qid, "the log corpus is seeded before discard")

	res := h.handleDiscardLogs(t.Context(), qid)
	require.False(t, res.IsError, "discard_logs: %s", toolResultText(res))

	assert.NotContains(t, fc.graphs, qid, "the named log graph is torn down")
	require.Len(t, fc.execs, 1, "discard fired exactly one Execute")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH, fc.execs[0].GetMutation().GetKind(),
		"discard still drives a DROP_GRAPH mutation (byte-unchanged logs path)")
	assert.Equal(t, "logs", fc.execs[0].GetTarget().GetGraph(), "discard targets graph=logs")
	assert.Equal(t, qid, fc.execs[0].GetTarget().GetName(), "discard targets the named log graph")
}
