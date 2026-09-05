// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// fakeSimilarityForcer implements the widened (async) SimilarityForcer. It records
// the thresholds/densify it was started with, scripts the started return + the
// completion mode (sync inline vs. stored-for-later), records the Begin/Finish args
// the handler threads through, and scripts the event-read node. The REPLACE-store
// round-trip modeling lives in the thought-package fake (Phase 2), not here — this
// fake only RECORDS that the handler threads startedAt/link/merge/rendered correctly.
type fakeSimilarityForcer struct {
	// StartSimilarityPass recording + scripting.
	gotLink, gotMerge float64
	gotDensify        clientthought.DensifyParams
	called            bool // StartSimilarityPass was invoked
	scriptStarted     bool // value StartSimilarityPass returns (true=started, false=coalesce)
	completeInline    bool // sync mode: invoke onComplete before returning
	completeRep       clientthought.SimilarityReport
	completeErr       error
	pendingComplete   clientthought.SimilarityComplete // non-sync mode: stored for the test to drive later

	// RunSimilarityPass (kept on the interface — the goroutine calls it internally).
	report clientthought.SimilarityReport
	err    error

	// BeginSimilarityEvent scripting + recording.
	beginID      string
	beginStarted time.Time
	beginErr     error
	beginCalled  bool
	beginLink    float64
	beginMerge   float64

	// FinishSimilarityEvent recording + scripting.
	finishErr        error
	finishCalled     bool
	finishID         string
	finishStartedAt  time.Time
	finishLink       float64
	finishMerge      float64
	finishStatus     string
	finishDurationMs int64
	finishRendered   string
	finishHeadline   map[string]string

	// Event-read scripting (drives the fetch-op + estimate tests).
	latestNode    *knowledgev1.Node
	latestOK      bool
	completedNode *knowledgev1.Node // latest COMPLETED event (drives the estimate test)
	completedOK   bool
	byIDNode      *knowledgev1.Node
	byIDOK        bool
}

func (f *fakeSimilarityForcer) RunSimilarityPass(_ context.Context, link, merge float64, densify clientthought.DensifyParams) (clientthought.SimilarityReport, error) {
	f.gotLink, f.gotMerge = link, merge
	f.gotDensify = densify
	return f.report, f.err
}

func (f *fakeSimilarityForcer) StartSimilarityPass(link, merge float64, densify clientthought.DensifyParams, onStarted func(), onComplete clientthought.SimilarityComplete) bool {
	f.called = true
	f.gotLink, f.gotMerge = link, merge
	f.gotDensify = densify
	if !f.scriptStarted {
		// Coalesce: neither callback fires (mirrors the real wrapper's !ok path).
		return false
	}
	if onStarted != nil {
		onStarted() // fires BeginSimilarityEvent synchronously, before returning.
	}
	if f.completeInline {
		if onComplete != nil {
			onComplete(context.Background(), f.completeRep, f.completeErr)
		}
	} else {
		f.pendingComplete = onComplete // non-sync: the test drives completion later.
	}
	return true
}

func (f *fakeSimilarityForcer) BeginSimilarityEvent(_ context.Context, link, merge float64) (string, time.Time, error) {
	f.beginCalled = true
	f.beginLink, f.beginMerge = link, merge
	return f.beginID, f.beginStarted, f.beginErr
}

func (f *fakeSimilarityForcer) FinishSimilarityEvent(_ context.Context, id string, startedAt time.Time, link, merge float64, status string, durationMs int64, rendered string, headline map[string]string) error {
	f.finishCalled = true
	f.finishID, f.finishStartedAt = id, startedAt
	f.finishLink, f.finishMerge = link, merge
	f.finishStatus, f.finishDurationMs = status, durationMs
	f.finishRendered, f.finishHeadline = rendered, headline
	return f.finishErr
}

func (f *fakeSimilarityForcer) LatestSimilarityEvent(_ context.Context) (*knowledgev1.Node, bool) {
	return f.latestNode, f.latestOK
}

func (f *fakeSimilarityForcer) LatestCompletedSimilarityEvent(_ context.Context) (*knowledgev1.Node, bool) {
	return f.completedNode, f.completedOK
}

func (f *fakeSimilarityForcer) SimilarityEventByID(_ context.Context, _ string) (*knowledgev1.Node, bool) {
	return f.byIDNode, f.byIDOK
}

// captureSlog redirects the default slog logger to a buffer for the duration of fn,
// restoring it afterward, and returns the captured text.
func captureSlog(fn func()) string {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// similarityDispatchDeps is a minimal ClientDeps exposing a SimilarityForcer.
type similarityDispatchDeps struct {
	forcer SimilarityForcer
	// propNotReady flips PropReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup) on the similarity / similarity_report
	// handlers. Zero value keeps the reflection loop ready.
	propNotReady bool
}

func (d similarityDispatchDeps) LocalLiveness() LocalLiveness          { return nil }
func (d similarityDispatchDeps) Sink() collector.Sink                  { return nil }
func (d similarityDispatchDeps) SubgraphFetcher() CloudSubgraphFetcher { return nil }
func (d similarityDispatchDeps) RootDir() string                       { return "" }
func (d similarityDispatchDeps) UsageAnalyzer() UsageAnalyzerAPI       { return nil }

func (d similarityDispatchDeps) PropReady() bool     { return !d.propNotReady }
func (d similarityDispatchDeps) PipelineReady() bool { return true }

func (d similarityDispatchDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d similarityDispatchDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d similarityDispatchDeps) BackendResolver() BackendResolver             { return nil }
func (d similarityDispatchDeps) GraphCaller() GraphCaller                     { return nil }
func (d similarityDispatchDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d similarityDispatchDeps) SegmentManager() SegmentSearcher              { return nil }
func (d similarityDispatchDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d similarityDispatchDeps) SegmentShipper() SegmentShipper               { return nil }
func (d similarityDispatchDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d similarityDispatchDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d similarityDispatchDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d similarityDispatchDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d similarityDispatchDeps) PipelineScanner() PipelineScanner         { return nil }

func (d similarityDispatchDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d similarityDispatchDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d similarityDispatchDeps) SimilarityForcer() SimilarityForcer       { return d.forcer }
func (d similarityDispatchDeps) BlindSpotProvider() BlindSpotProvider     { return nil }
func (d similarityDispatchDeps) ClusterProvider() ClusterProvider         { return nil }
func (d similarityDispatchDeps) TensionsProvider() TensionsProvider       { return nil }

func callPropagate(deps ClientDeps, args string) kgtools.ToolResult {
	return handlePropagateClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: []byte(args),
	})
}

// TestSimilarityDispatch_RoutesWithParsedThresholds: similarity:true routes to the
// forcer; flexFloat accepts both "0.9" (string) and 0.9 (number).
func TestSimilarityDispatch_RoutesWithParsedThresholds(t *testing.T) {
	// Numeric form — including the densify overrides (flexFloat threshold, flexInt k +
	// budget). Asserts the parsed densify values REACH SimilarityForcer.RunSimilarityPass.
	f1 := &fakeSimilarityForcer{}
	callPropagate(similarityDispatchDeps{forcer: f1}, `{"similarity":true,"link_threshold":0.9,"merge_threshold":0.97,"densify_threshold":0.94,"densify_k":3,"densify_edge_budget":500}`)
	require.True(t, f1.called, "similarity:true must route to the SimilarityForcer")
	assert.InDelta(t, 0.9, f1.gotLink, 1e-9)
	assert.InDelta(t, 0.97, f1.gotMerge, 1e-9)
	assert.InDelta(t, 0.94, f1.gotDensify.Threshold, 1e-9, "densify_threshold must reach the forcer")
	assert.Equal(t, 3, f1.gotDensify.K, "densify_k must reach the forcer")
	assert.Equal(t, 500, f1.gotDensify.EdgeBudget, "densify_edge_budget must reach the forcer")

	// String form (double-encoded) — flexFloat/flexInt must parse the quoted forms.
	f2 := &fakeSimilarityForcer{}
	callPropagate(similarityDispatchDeps{forcer: f2}, `{"similarity":"true","link_threshold":"0.85","merge_threshold":"0.95","densify_threshold":"0.88","densify_k":"4","densify_edge_budget":"250"}`)
	require.True(t, f2.called)
	assert.InDelta(t, 0.85, f2.gotLink, 1e-9)
	assert.InDelta(t, 0.95, f2.gotMerge, 1e-9)
	assert.InDelta(t, 0.88, f2.gotDensify.Threshold, 1e-9, "quoted densify_threshold parses")
	assert.Equal(t, 4, f2.gotDensify.K, "quoted densify_k parses")
	assert.Equal(t, 250, f2.gotDensify.EdgeBudget, "quoted densify_edge_budget parses")

	// Absent densify args → zero-value DensifyParams reaches the forcer (the lever
	// resolves zero→default internally).
	f3 := &fakeSimilarityForcer{}
	callPropagate(similarityDispatchDeps{forcer: f3}, `{"similarity":true}`)
	require.True(t, f3.called)
	assert.Equal(t, clientthought.DensifyParams{}, f3.gotDensify, "absent densify args → zero-value DensifyParams")
}

// TestSimilarityDispatch_MalformedDensifyLoud: a malformed densify arg (densify_k
// not an int) returns a LOUD errorResult naming the bad value and does NOT run the
// lever — mirroring the malformed-threshold guard.
func TestSimilarityDispatch_MalformedDensifyLoud(t *testing.T) {
	f := &fakeSimilarityForcer{}
	res := callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true,"densify_k":"not-an-int"}`)
	assert.True(t, res.IsError, "a malformed densify_k must return an error result")
	assert.False(t, f.called, "the lever must NOT run on a malformed densify arg")
	assert.Contains(t, resultText(res), "not-an-int", "the error must name the offending value")
}

// TestSimilarityDispatch_MalformedThresholdLoud: a malformed threshold returns a
// LOUD errorResult and does NOT run the lever.
func TestSimilarityDispatch_MalformedThresholdLoud(t *testing.T) {
	f := &fakeSimilarityForcer{}
	res := callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true,"link_threshold":"not-a-number"}`)
	assert.True(t, res.IsError, "a malformed threshold must return an error result")
	assert.False(t, f.called, "the lever must NOT run on a malformed threshold")
	assert.Contains(t, resultText(res), "not-a-number", "the error must name the offending value")
}

// TestSimilarityTrigger_AlreadyRunningContract: a second trigger while a pass is
// running (StartSimilarityPass returns started=false) yields the "already running"
// message PLUS the same fetch/estimate contract, and does NOT start a second pass
// (no Finish is driven — onStarted/onComplete never fire on the coalesce path).
func TestSimilarityTrigger_AlreadyRunningContract(t *testing.T) {
	f := &fakeSimilarityForcer{scriptStarted: false} // coalesce
	res := callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	assert.False(t, res.IsError, "a coalesce is benign, not an error")
	body := resultText(res)
	assert.Contains(t, body, "already running", "the coalesce message names the in-flight pass")
	// Same fetch/estimate contract as the started path.
	assert.Contains(t, body, `thoughts({"operation":"similarity_report"})`, "verbatim fetch call present on coalesce")
	assert.Contains(t, body, "Estimated duration", "estimate present on coalesce")
	assert.Contains(t, body, "MAY take LONGER", "may-take-longer caveat present on coalesce")
	// No event created, no Finish driven (onStarted/onComplete never fire on coalesce).
	assert.False(t, f.beginCalled, "coalesce must NOT create a running event")
	assert.False(t, f.finishCalled, "coalesce must NOT drive a completion")
}

// The renderSimilarityReport OUTPUT tests (TestSimilarityReport_Render and the two
// densify variants) live in similarity_report_render_test.go — they build a report
// directly and need none of this file's fixtures.

// startedForcer returns a fake scripted to START a pass (uncontended acquire), with
// a Begin id/startedAt so the trigger-time event is created.
func startedForcer() *fakeSimilarityForcer {
	return &fakeSimilarityForcer{
		scriptStarted: true,
		beginID:       "evt-123",
		beginStarted:  time.Now(),
	}
}

// TestSimilarityTrigger_ResponseContract: the trigger response carries all three
// contract elements — the verbatim fetch call, a duration estimate, and an explicit
// may-take-longer statement.
func TestSimilarityTrigger_ResponseContract(t *testing.T) {
	f := startedForcer()
	res := callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	assert.False(t, res.IsError)
	body := resultText(res)
	assert.Contains(t, body, `thoughts({"operation":"similarity_report"})`, "verbatim copy-pasteable fetch call")
	assert.Contains(t, body, "Estimated duration", "duration estimate present")
	assert.Contains(t, body, "MAY take LONGER", "explicit may-take-longer statement")
}

// TestSimilarityTrigger_ReturnsImmediately: a started pass whose onComplete is NOT
// invoked synchronously (stored for later) still returns promptly — the handler does
// not block on summarization.
func TestSimilarityTrigger_ReturnsImmediately(t *testing.T) {
	f := startedForcer() // completeInline=false → onComplete stored, not called
	done := make(chan kgtools.ToolResult, 1)
	go func() {
		done <- callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	}()
	select {
	case res := <-done:
		assert.False(t, res.IsError)
		assert.Contains(t, resultText(res), "STARTED", "the trigger returns the started message immediately")
	case <-time.After(2 * time.Second):
		t.Fatal("handleSimilarityPass blocked instead of returning immediately")
	}
	require.NotNil(t, f.pendingComplete, "onComplete was stored (pass still pending), proving the handler did not await it")
}

// TestSimilarityTrigger_EventLifecycle: the running event is created at trigger time
// (Begin invoked before the handler returns) and the completion callback persists
// status + duration_ms + rendered report (Finish invoked with the rendered text).
func TestSimilarityTrigger_EventLifecycle(t *testing.T) {
	f := startedForcer()
	f.completeInline = true // drive onComplete inline so Finish records this turn
	f.completeRep = clientthought.SimilarityReport{TopicCount: 5, LinkThreshold: 0.9, MergeThreshold: 0.97}

	res := callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true,"link_threshold":0.9,"merge_threshold":0.97}`)
	assert.False(t, res.IsError)

	require.True(t, f.beginCalled, "BeginSimilarityEvent created the running event at trigger time")
	assert.InDelta(t, 0.9, f.beginLink, 1e-9, "Begin received the link threshold")
	require.True(t, f.finishCalled, "FinishSimilarityEvent persisted the completion")
	assert.Equal(t, "evt-123", f.finishID, "Finish carries the running record's id")
	assert.Equal(t, "completed", f.finishStatus)
	assert.Contains(t, f.finishRendered, "# Topic Similarity Pass", "Finish carries the rendered report text")
	assert.NotNil(t, f.finishHeadline, "Finish carries the headline counts")
	assert.Equal(t, "5", f.finishHeadline[clientthought.MetaSimTopics], "headline topic count threaded through")
}

// TestSimilarityTrigger_BeginFailure: a Begin error (fired via onStarted) does NOT
// abort the pass — StartSimilarityPass still runs to onComplete, the handler still
// returns started with all three contract elements, slogs the Begin error, and
// onComplete sees eventID=="" so it skips Finish.
func TestSimilarityTrigger_BeginFailure(t *testing.T) {
	f := startedForcer()
	f.beginErr = errors.New("begin boom") // Begin fails
	f.completeInline = true               // drive onComplete inline

	var res kgtools.ToolResult
	out := captureSlog(func() {
		res = callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	})

	assert.False(t, res.IsError, "a Begin failure must not turn the trigger into an error")
	body := resultText(res)
	assert.Contains(t, body, "STARTED")
	assert.Contains(t, body, `thoughts({"operation":"similarity_report"})`)
	assert.Contains(t, body, "Estimated duration")
	assert.Contains(t, body, "MAY take LONGER")
	assert.Contains(t, out, "BeginSimilarityEvent failed", "the Begin error is slogged")
	assert.False(t, f.finishCalled, "onComplete saw eventID=='' and skipped Finish")
}

// TestSimilarityTrigger_LogsRenderedReport: the completion path slogs the full
// rendered report as an audit trail (the report header appears in the log).
func TestSimilarityTrigger_LogsRenderedReport(t *testing.T) {
	f := startedForcer()
	f.completeInline = true
	f.completeRep = clientthought.SimilarityReport{TopicCount: 2}

	out := captureSlog(func() {
		callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	})
	assert.Contains(t, out, "# Topic Similarity Pass", "the full rendered report is slogged for audit")
}

// TestSimilarityTrigger_PersistFailureDegrades: a Finish error degrades loudly (the
// callback slogs Error) but does NOT panic or fail the pass — the goroutine still
// completes and the trigger response is unaffected.
func TestSimilarityTrigger_PersistFailureDegrades(t *testing.T) {
	f := startedForcer()
	f.completeInline = true
	f.finishErr = errors.New("persist boom") // persistence fails

	var res kgtools.ToolResult
	out := captureSlog(func() {
		res = callPropagate(similarityDispatchDeps{forcer: f}, `{"similarity":true}`)
	})
	assert.False(t, res.IsError, "a persistence failure must not fail the trigger")
	require.True(t, f.finishCalled, "Finish was attempted")
	assert.Contains(t, out, "FinishSimilarityEvent failed", "the persistence failure is slogged loudly")
}

// (the similarity_report fetch-op tests + the estimate-helper tests live in
// similarity_report_test.go, which shares this file's fakeSimilarityForcer.)

// TestSimilarityTrigger_CancelledCallStartsNoPass: a cancelled call must start NO
// pass, create NO running event, and return a loud error. Scoped to cancellation
// BEFORE the pass starts; an already-started pass is not abortable by a client
// cancel (accepted residual). Direct call: callPropagate hardcodes Background().
func TestSimilarityTrigger_CancelledCallStartsNoPass(t *testing.T) {
	f := startedForcer() // scripted to ACQUIRE, so a pass WOULD start
	ctx, cancel := context.WithCancel(opCtx())
	cancel()
	res := handlePropagateClient(ctx, similarityDispatchDeps{forcer: f}, kgtools.CallToolParams{
		Name: "thoughts", Arguments: []byte(`{"similarity":true}`)})
	assert.False(t, f.called, "a cancelled call must start NO pass")
	assert.False(t, f.beginCalled, "a cancelled call must create NO running event")
	assert.True(t, res.IsError, "a cancelled call must return a loud error, not a started message")
}
