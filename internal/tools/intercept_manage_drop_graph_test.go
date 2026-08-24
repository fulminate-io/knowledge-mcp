// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// dropGraphCall drives InterceptManage(drop_graph) over a fakeGraphCaller wired
// as the deps GraphCaller. The fake records every ExecuteRequest in execRequests
// (and the MutationPlan in execMutations), so the tests assert the exact
// DROP_GRAPH envelope + selector the client lowered. mutateAffected is returned
// as the Execute affected_count so the ack-count rendering is observable.
func dropGraphCall(t *testing.T, fc *fakeGraphCaller, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	deps := interceptTestDeps{gc: fc}
	return InterceptManage(opCtx(), deps, kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

// fakeCacheDropper records the (graphType, name) the handler resolved and how many
// times it was called, and returns a programmable report + error. The call count is
// what lets the dry-run test assert the dropper was never REACHED — an assertion on
// the rendered text alone cannot tell a preview that removed nothing from one that
// removed files and then described them as hypothetical.
type fakeCacheDropper struct {
	report DropGraphCacheReport
	err    error

	calls  int
	gotGT  kgtypes.GraphType
	gotNam string
}

func (f *fakeCacheDropper) DropGraphCache(gt kgtypes.GraphType, name string) (DropGraphCacheReport, error) {
	f.calls++
	f.gotGT = gt
	f.gotNam = name
	return f.report, f.err
}

// dropGraphTestDeps embeds interceptTestDeps (PipelineReady()==true, GraphCaller()
// returns gc) and overrides SegmentCacheDropper() so the handler reaches the fake.
// A nil dropper exercises the no-segment-engine degraded path.
type dropGraphTestDeps struct {
	interceptTestDeps
	dropper SegmentCacheDropper
}

func (d dropGraphTestDeps) SegmentCacheDropper() SegmentCacheDropper { return d.dropper }

// dropGraphCallWithDropper is dropGraphCall with a SegmentCacheDropper wired in.
func dropGraphCallWithDropper(
	t *testing.T, fc *fakeGraphCaller, dropper SegmentCacheDropper, args string,
) (bool, kgtools.ToolResult) {
	t.Helper()
	deps := dropGraphTestDeps{interceptTestDeps: interceptTestDeps{gc: fc}, dropper: dropper}
	return InterceptManage(opCtx(), deps, kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
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
	// The Execute affected_count is deliberately NOT rendered: it is the server's
	// one-graph handle count, not a node count. See
	// TestInterceptManage_DropGraph_MessageDoesNotClaimNodeCount.
	assert.Contains(t, body, "server-side graph removed", "the ack states what actually happened")
}

// TestInterceptManage_DropGraph_MessageDoesNotClaimNodeCount asserts the ack
// never reports a node count. The server returns ONE handle for the dropped
// graph, and rendering that 1 as "(1 node(s) removed)" tells an operator who
// just dropped a 40k-node graph that one node went away. The "Dropped graph"
// prefix survives (the executed path must still claim the drop); only the
// bogus count phrasing goes.
func TestInterceptManage_DropGraph_MessageDoesNotClaimNodeCount(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 1}
	handled, res := dropGraphCall(t, fc, `{"operation":"drop_graph","graph":"hellograph","name":"demo"}`)
	require.True(t, handled, "drop_graph must be handled by InterceptManage")
	require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

	body := toolResultText(res)
	assert.Contains(t, body, "Dropped graph hellograph/demo", "the executed path still claims the drop")
	assert.NotContains(t, body, "node(s) removed",
		"the server's one-graph handle count must not be rendered as a node count")
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

// TestInterceptManage_DropGraph_BranchKeyTargetsRepo is a CHARACTERIZATION GUARD —
// green before and after — pinning that a drop naming a composed branch graph key
// addresses the BRANCH GRAPH and not its base.
//
// It exists because the coverage table now enumerates branch graphs, so the Graphs
// page offers a delete control on a branch row for the first time. The routing that
// makes that delete correct was previously exercised by nothing.
//
// THE EMPTY-BRANCH LEG IS THE ONE THAT MATTERS. Asserting only Repo passes against a
// selector that ALSO set Branch — which the server Scopes into a base+overlay
// composite, changing which graph the drop identity derives from. The failure mode
// this pins is a destructive one: an operator asks to delete a branch and loses main.
func TestInterceptManage_DropGraph_BranchKeyTargetsRepo(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 1}
	handled, res := dropGraphCall(t, fc,
		`{"operation":"drop_graph","graph":"code","name":"agent@launch-fixes"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

	require.Len(t, fc.execRequests, 1, "exactly one Execute RPC")
	tgt := fc.execRequests[0].GetTarget()
	assert.Equal(t, "code", tgt.GetGraph())
	assert.Equal(t, "agent@launch-fixes", tgt.GetRepo(),
		"the composed branch key rides Repo verbatim — that is what makes the server resolve the overlay")
	assert.Empty(t, tgt.GetBranch(),
		"Branch must stay EMPTY: a Repo+Branch selector Scopes the base+overlay composite instead")

	assert.Contains(t, toolResultText(res), "Dropped graph code/agent@launch-fixes",
		"the ack names the branch graph the operator deleted, not the bare repo")
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
	dropper := &fakeCacheDropper{}
	handled, res := dropGraphCallWithDropper(t, fc, dropper,
		`{"operation":"drop_graph","graph":"hellograph","name":"demo","dry_run":true}`)
	require.True(t, handled)
	require.False(t, res.IsError, "dry_run preview is not an error: %s", toolResultText(res))

	assert.Empty(t, fc.execRequests, "dry_run issues ZERO Execute mutations")
	// This and the NotContains below catch the SAME defect — cleanup hoisted above
	// the dry-run branch — from opposite sides: this one notices the preview
	// actually PERFORMED a removal, the NotContains notices it started CLAIMING one.
	// Neither subsumes the other.
	require.Equal(t, 0, dropper.calls, "dry_run must never reach the local cache dropper")
	body := toolResultText(res)
	assert.Contains(t, body, "DRY RUN", "the preview is labeled DRY RUN")
	assert.Contains(t, body, "would drop graph hellograph/demo", "verb is 'would drop'")
	assert.NotContains(t, body, "Dropped graph", "dry_run must NOT claim a completed drop")
}

// TestInterceptManage_DropGraph_RemovesLocalSegmentCache asserts the handler drives
// the local L2 teardown with the RESOLVED cache target and renders the true totals.
// The knowledge sub-case is the normalization catcher: the knowledge cache lives at
// <root>/<format>/knowledge/default, so an empty name must reach the dropper as
// "default" or the teardown silently misses every knowledge-graph artifact.
func TestInterceptManage_DropGraph_RemovesLocalSegmentCache(t *testing.T) {
	t.Run("knowledge normalizes the empty name to default", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateAffected: 1}
		dropper := &fakeCacheDropper{report: DropGraphCacheReport{
			Formats: []string{"hnsw", "bm25", "rebuildstate"},
			Files:   7,
			Bytes:   4096,
		}}
		handled, res := dropGraphCallWithDropper(t, fc, dropper, `{"operation":"drop_graph","graph":"knowledge"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

		require.Equal(t, 1, dropper.calls, "the dropper is driven exactly once")
		assert.Equal(t, kgtypes.GraphKnowledge, dropper.gotGT)
		assert.Equal(t, "default", dropper.gotNam, "knowledge with no name keys the cache as default")

		body := toolResultText(res)
		assert.Contains(t, body, "server-side graph removed")
		assert.Contains(t, body, "local segment cache: 7 file(s), 4096 bytes")
		assert.NotContains(t, body, "node(s) removed")
	})

	t.Run("code passes the repo name through unchanged", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateAffected: 1}
		dropper := &fakeCacheDropper{report: DropGraphCacheReport{
			Formats: []string{"hnsw"}, Files: 3, Bytes: 512,
		}}
		handled, res := dropGraphCallWithDropper(t, fc, dropper,
			`{"operation":"drop_graph","graph":"code","name":"demoRepo"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

		assert.Equal(t, kgtypes.GraphCode, dropper.gotGT)
		assert.Equal(t, "demoRepo", dropper.gotNam, "non-knowledge families need no normalization")
		assert.Contains(t, toolResultText(res), "local segment cache: 3 file(s), 512 bytes")
	})

	t.Run("a graph never cached locally reports no artifacts", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateAffected: 1}
		dropper := &fakeCacheDropper{} // zero report — the never-loaded case
		handled, res := dropGraphCallWithDropper(t, fc, dropper,
			`{"operation":"drop_graph","graph":"hellograph","name":"demo"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

		body := toolResultText(res)
		assert.Contains(t, body, "Dropped graph hellograph/demo", "the drop still completed")
		assert.Contains(t, body, "no local segment cache artifacts found")
		assert.NotContains(t, body, "node(s) removed")
	})
}

// TestInterceptManage_DropGraph_NoLocalDropperStillDropsServerSide is the
// degraded-client catcher: a client with no segment engine must still complete the
// server-side drop and SAY the cache went uninspected. Without this, a nil
// dereference or an early error return would report a drop that genuinely
// succeeded as a failure.
func TestInterceptManage_DropGraph_NoLocalDropperStillDropsServerSide(t *testing.T) {
	fc := &fakeGraphCaller{mutateAffected: 1}
	handled, res := dropGraphCallWithDropper(t, fc, nil,
		`{"operation":"drop_graph","graph":"hellograph","name":"demo"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "a missing local segment engine is not a drop failure: %s", toolResultText(res))

	require.Len(t, fc.execRequests, 1, "the server-side DROP_GRAPH still fired")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH,
		fc.execRequests[0].GetMutation().GetKind())

	body := toolResultText(res)
	assert.Contains(t, body, "Dropped graph hellograph/demo")
	assert.Contains(t, body, "local segment cache not inspected")
	assert.NotContains(t, body, "node(s) removed")
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
