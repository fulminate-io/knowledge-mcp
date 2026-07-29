// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// (fakeSimilarityForcer, similarityDispatchDeps, and resultText live in
// similarity_dispatch_test.go — this file tests the similarity_report fetch op +
// the estimate helper against that same fake.)

// TestSimilarityEstimate_DefaultRange (FAILS-WHEN-ABSENT, ticket Defect 2): with no
// completed pass to calibrate against, the coarse default range renders.
func TestSimilarityEstimate_DefaultRange(t *testing.T) {
	f := &fakeSimilarityForcer{completedOK: false} // no completed event
	est := similarityEstimate(f, context.Background())
	assert.Equal(t, similarityDefaultEstimate, est, "no completed pass → coarse default range")
}

// TestSimilarityEstimate_FromLastDuration: with a completed pass carrying duration_ms,
// the estimate reflects that duration (read via LatestCompletedSimilarityEvent).
func TestSimilarityEstimate_FromLastDuration(t *testing.T) {
	n := &knowledgev1.Node{Id: "evt-prior", Type: "event"}
	kgtypes.SetValue(n, clientthought.MetaSimStatus, clientthought.SimStatusCompleted)
	kgtypes.SetValue(n, clientthought.MetaSimDurationMs, "300000") // 5 minutes
	f := &fakeSimilarityForcer{completedNode: n, completedOK: true}

	est := similarityEstimate(f, context.Background())
	assert.Contains(t, est, "last completed pass", "the estimate references the prior pass")
	assert.Contains(t, est, "5m0s", "the estimate reflects the prior duration (5 minutes)")
}

// TestSimilarityEstimate_IgnoresRunningEvent (FAILS-WHEN-ABSENT, ticket Defect 2):
// models the real blinding scenario — a completed pass (duration_ms≈99000) AND a newer
// running event (the record the trigger just wrote, no duration_ms). The estimate reads
// the latest COMPLETED event, so it reflects ~99s, NOT the default. Fails if the
// estimate reverts to reading the latest ANY-status event (which would find the running
// record and fall back to the default forever).
func TestSimilarityEstimate_IgnoresRunningEvent(t *testing.T) {
	completed := &knowledgev1.Node{Id: "evt-done", Type: "event"}
	kgtypes.SetValue(completed, clientthought.MetaSimStatus, clientthought.SimStatusCompleted)
	kgtypes.SetValue(completed, clientthought.MetaSimDurationMs, "99000") // ~99 seconds

	// A newer running event — what LatestSimilarityEvent (latest-ANY) would surface.
	running := &knowledgev1.Node{Id: "evt-running", Type: "event"}
	kgtypes.SetValue(running, clientthought.MetaSimStatus, clientthought.SimStatusRunning)

	f := &fakeSimilarityForcer{
		completedNode: completed, completedOK: true, // the latest COMPLETED event
		latestNode: running, latestOK: true, // a newer running event (must be ignored by the estimate)
	}

	est := similarityEstimate(f, context.Background())
	assert.Contains(t, est, "1m39s", "the estimate reflects the completed pass duration (~99s), not the running event")
	assert.NotEqual(t, similarityDefaultEstimate, est, "a newer running event must NOT blind the estimate")
}

// completedEventNode builds a completed similarity event whose content is the
// rendered report body.
func completedEventNode(report string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: "evt-c", Type: "event", Content: report}
	kgtypes.SetValue(n, clientthought.MetaSimStatus, clientthought.SimStatusCompleted)
	return n
}

func callSimilarityReport(deps ClientDeps, args string) kgtools.ToolResult {
	return handleSimilarityReportClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: []byte(args),
	})
}

// TestSimilarityReport_NotReadyGate (FAILS-WHEN-ABSENT) proves the bind-first
// wiring-window gate (bind-first startup): with PropReady()=false, similarity_report returns
// the uniform "daemon still starting" error rather than the misleading "not
// running in this process" message — even with a live forcer present (the
// readiness check fires BEFORE the nil-forcer check).
func TestSimilarityReport_NotReadyGate(t *testing.T) {
	f := &fakeSimilarityForcer{}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f, propNotReady: true}, `{}`)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "daemon still starting")
	assert.NotContains(t, toolResultText(res), "not running in this process")
}

// TestSimilarityReport_CompletedReturnsReport: a completed event's content (the
// rendered report) is returned verbatim.
func TestSimilarityReport_CompletedReturnsReport(t *testing.T) {
	report := "# Topic Similarity Pass\n\n- Surviving topics: 9\n"
	f := &fakeSimilarityForcer{latestNode: completedEventNode(report), latestOK: true}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f}, `{}`)
	assert.False(t, res.IsError)
	assert.Equal(t, report, resultText(res), "completed → the full rendered report verbatim")
}

// TestSimilarityReport_FailedLoud: a failed event surfaces the failure loudly.
func TestSimilarityReport_FailedLoud(t *testing.T) {
	n := &knowledgev1.Node{Id: "evt-f", Type: "event", Content: "stage errors: drain failed: boom"}
	kgtypes.SetValue(n, clientthought.MetaSimStatus, clientthought.SimStatusFailed)
	f := &fakeSimilarityForcer{latestNode: n, latestOK: true}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f}, `{}`)
	assert.True(t, res.IsError, "a failed pass surfaces as an error result")
	body := resultText(res)
	assert.Contains(t, body, "FAILED")
	assert.Contains(t, body, "drain failed: boom", "the failure detail is named")
}

// TestSimilarityReport_RunningState: a running event renders in-progress + elapsed +
// estimate.
func TestSimilarityReport_RunningState(t *testing.T) {
	n := &knowledgev1.Node{Id: "evt-r", Type: "event"}
	kgtypes.SetValue(n, clientthought.MetaSimStatus, clientthought.SimStatusRunning)
	kgtypes.SetValue(n, clientthought.MetaSimStartedAt, time.Now().Add(-30*time.Second).Format(time.RFC3339))
	f := &fakeSimilarityForcer{latestNode: n, latestOK: true}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f}, `{}`)
	assert.False(t, res.IsError)
	body := resultText(res)
	assert.Contains(t, body, "IN PROGRESS", "running → in-progress")
	assert.Contains(t, body, "elapsed", "running → elapsed time")
	assert.Contains(t, body, "Estimated duration", "running → estimate")
}

// TestSimilarityReport_ByID: an optional id fetches a specific past pass via
// SimilarityEventByID.
func TestSimilarityReport_ByID(t *testing.T) {
	report := "# Topic Similarity Pass\n\n- by id\n"
	byID := completedEventNode(report)
	// latest returns nothing — only the by-id path can produce a result here.
	f := &fakeSimilarityForcer{byIDNode: byID, byIDOK: true, latestOK: false}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f}, `{"id":"evt-c"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, report, resultText(res), "by-id fetch renders the scripted node (SimilarityEventByID routing)")
}

// TestSimilarityReport_EmptyState: no pass ever → a clear empty-state message (not an
// error, not blank).
func TestSimilarityReport_EmptyState(t *testing.T) {
	f := &fakeSimilarityForcer{latestOK: false}
	res := callSimilarityReport(similarityDispatchDeps{forcer: f}, `{}`)
	assert.False(t, res.IsError, "empty state is guidance, not an error")
	body := resultText(res)
	assert.Contains(t, body, "No similarity pass has run yet", "clear empty-state guidance")
	assert.Contains(t, body, `thoughts({"operation":"propagate","similarity":true})`, "names how to trigger one")
}

// TestInterceptThoughts_RoutesSimilarityReport: operation=similarity_report is
// CLAIMED by InterceptThoughts (handled=true) and reaches handleSimilarityReportClient.
func TestInterceptThoughts_RoutesSimilarityReport(t *testing.T) {
	report := "# Topic Similarity Pass\n\n- routed\n"
	f := &fakeSimilarityForcer{latestNode: completedEventNode(report), latestOK: true}
	handled, res := InterceptThoughts(opCtx(), similarityDispatchDeps{forcer: f}, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: []byte(`{"operation":"similarity_report"}`),
	})
	require.True(t, handled, "similarity_report must be claimed by the client intercept")
	assert.False(t, res.IsError)
	assert.Equal(t, report, resultText(res), "the call reached handleSimilarityReportClient and rendered the event")
}

// TestThoughtsSchema_SimilarityReportOperation pins that the thoughts tool schema
// advertises similarity_report in the operation enum and documents the op + the
// now-async propagate similarity trigger in the description.
func TestThoughtsSchema_SimilarityReportOperation(t *testing.T) {
	def := ThoughtsToolDef()

	opProp, ok := def.InputSchema.Properties["operation"]
	require.True(t, ok, "operation property must exist")
	assert.Contains(t, opProp.Enum, "similarity_report", "operation enum must advertise similarity_report")

	assert.Contains(t, def.Description, "similarity_report", "tool description must document the similarity_report op")
	assert.Contains(t, def.Description, "ASYNCHRONOUSLY", "tool description must state the similarity trigger is async")
}
