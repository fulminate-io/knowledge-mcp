// SPDX-License-Identifier: Apache-2.0

// worker_test.go — unit tests for the client-side worker intercept.
// Uses a fakeRuntime + workerTestDeps that satisfy WorkerRuntimeAPI and
// ClientDeps directly — no httptest, no real *dream.Runner. Failure-mode
// pins:
//
//   - trigger / status route through the runtime (intercept fires)
//   - list / create / update / delete fall through to the server
//   - empty name on trigger / status surfaces an error result
//   - nil runtime surfaces a degraded-runtime error (Phase G left
//     wireWorkerRuntime nil-tolerant; the intercept must too)
//   - non-worker tool names return (false, zero) — no fallthrough
//     interference

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
)

// fakeRuntime satisfies WorkerRuntimeAPI without instantiating a real
// *dream.Runner. Records every call so tests can pin payload + limit
// values, and lets each test override the canned return values.
type fakeRuntime struct {
	mu sync.Mutex

	triggerCalls   []fakeTriggerCall
	triggerErr     error
	statusCalls    []fakeStatusCall
	statusRecords  []dream.InvocationRecord
	statusErr      error
	cancelCalls    []fakeCancelCall
	cancelCount    int
	cancelErr      error
	runningOut     []dream.RunningInvocation
	byNameNotFound bool  // when true, ByName reports !found for every name
	byNameErr      error // when set, ByName returns this error
}

type fakeCancelCall struct {
	invocation string
	name       string
}

type fakeTriggerCall struct {
	name    string
	payload json.RawMessage
}

type fakeStatusCall struct {
	name  string
	limit int
}

func (f *fakeRuntime) OnManualTrigger(_ context.Context, name string, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Defensive copy so the test sees the bytes the intercept passed in
	// even if the caller mutates the slice afterward.
	pcopy := make(json.RawMessage, len(payload))
	copy(pcopy, payload)
	f.triggerCalls = append(f.triggerCalls, fakeTriggerCall{name: name, payload: pcopy})
	return f.triggerErr
}

func (f *fakeRuntime) Status(_ context.Context, name string, limit int) ([]dream.InvocationRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls = append(f.statusCalls, fakeStatusCall{name: name, limit: limit})
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return append([]dream.InvocationRecord(nil), f.statusRecords...), nil
}

// ByName satisfies the existence pre-flight added to handleWorkerStatus.
// Defaults to (Worker{}, true, nil) — "worker exists" — so existing tests
// pass without per-test setup. Tests that need to exercise the not-found
// path can set byNameNotFound or byNameErr.
func (f *fakeRuntime) ByName(_ context.Context, name string) (dream.Worker, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byNameErr != nil {
		return dream.Worker{}, false, f.byNameErr
	}
	if f.byNameNotFound {
		return dream.Worker{}, false, nil
	}
	return dream.Worker{Name: name}, true, nil
}

func (f *fakeRuntime) Running() []dream.RunningInvocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dream.RunningInvocation(nil), f.runningOut...)
}

func (f *fakeRuntime) Cancel(invocation, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, fakeCancelCall{invocation: invocation, name: name})
	if f.cancelErr != nil {
		return 0, f.cancelErr
	}
	return f.cancelCount, nil
}

// workerTestDeps satisfies ClientDeps without any GraphClient / Sink
// plumbing. Only WorkerRuntime() / WorkerCRUD() are exercised by
// InterceptWorker. The runtime and crud fields are interfaces directly
// so tests can pass a fake, an explicit nil interface, or a typed-nil
// pointer wrapped as a fake variant.
type workerTestDeps struct {
	runtime WorkerRuntimeAPI
	crud    WorkerCRUDAPI
	// notReady flips WorkerReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup). Zero value keeps the worker
	// runtime ready, so every pre-existing test exercises the wired path.
	notReady bool
}

func (d workerTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d workerTestDeps) Sink() collector.Sink                         { return nil }
func (d workerTestDeps) RootDir() string                              { return "" }
func (d workerTestDeps) WorkerRuntime() WorkerRuntimeAPI              { return d.runtime }
func (d workerTestDeps) WorkerReady() bool                            { return !d.notReady }
func (d workerTestDeps) PropReady() bool                              { return true }
func (d workerTestDeps) PipelineReady() bool                          { return true }
func (d workerTestDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d workerTestDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d workerTestDeps) WorkerCRUD() WorkerCRUDAPI                    { return d.crud }
func (d workerTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d workerTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d workerTestDeps) BackendResolver() BackendResolver             { return nil }
func (d workerTestDeps) GraphCaller() GraphCaller                     { return nil }
func (d workerTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d workerTestDeps) RepoResolver() *RepoResolver                  { return nil }
func (d workerTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d workerTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d workerTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d workerTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d workerTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d workerTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d workerTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

// callWorker invokes InterceptWorker with the given JSON args and
// returns the (handled, body, isError) tuple. Mirrors callAst's shape.
func callWorker(t *testing.T, deps ClientDeps, argsJSON string) (handled bool, body string, isErr bool) {
	t.Helper()
	params := kgtools.CallToolParams{
		Name:      "worker",
		Arguments: json.RawMessage(argsJSON),
	}
	h, res := InterceptWorker(deps, params)
	if !h {
		return false, "", false
	}
	require.NotEmpty(t, res.Content, "intercept handled but returned no content")
	return true, res.Content[0].Text, res.IsError
}

// TestInterceptWorker_NameFiltering pins that InterceptWorker returns
// (false, zero) for any tool other than "worker" — must not interfere
// with the fallthrough to other intercepts.
func TestInterceptWorker_NameFiltering(t *testing.T) {
	deps := workerTestDeps{runtime: &fakeRuntime{}}
	for _, name := range []string{"ast", "collect", "manage", "search", "query", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptWorker(deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptWorker", name)
		assert.Empty(t, res.Content, "non-worker call must return zero ToolResult")
	}
}

// TestInterceptWorker_TriggerHappyPath pins that operation=trigger
// dispatches through the runtime with the parsed name + payload, and
// returns the same "triggered" status string the server-side handler
// produces.
func TestInterceptWorker_TriggerHappyPath(t *testing.T) {
	rt := &fakeRuntime{}
	deps := workerTestDeps{runtime: rt}
	handled, body, isErr := callWorker(t, deps, `{"operation":"trigger","name":"smoke","payload":{"q":"hi"}}`)
	require.True(t, handled, "trigger op must be handled client-side")
	require.False(t, isErr, "expected non-error result, got: %s", body)
	assert.Contains(t, body, `worker "smoke" triggered`)
	assert.Contains(t, body, "running asynchronously")

	require.Len(t, rt.triggerCalls, 1, "OnManualTrigger called exactly once")
	assert.Equal(t, "smoke", rt.triggerCalls[0].name)
	assert.JSONEq(t, `{"q":"hi"}`, string(rt.triggerCalls[0].payload))
}

// TestInterceptWorker_TriggerEmptyPayload pins that an absent payload
// becomes JSON null at the runtime boundary — matches the server-side
// handleWorkerTrigger which substitutes RawMessage("null") for empty
// payloads so the runtime always receives valid JSON.
func TestInterceptWorker_TriggerEmptyPayload(t *testing.T) {
	rt := &fakeRuntime{}
	deps := workerTestDeps{runtime: rt}
	handled, _, isErr := callWorker(t, deps, `{"operation":"trigger","name":"smoke"}`)
	require.True(t, handled)
	require.False(t, isErr)
	require.Len(t, rt.triggerCalls, 1)
	assert.JSONEq(t, "null", string(rt.triggerCalls[0].payload))
}

// TestInterceptWorker_TriggerEmptyName pins that operation=trigger with
// an empty / whitespace name surfaces an error result (handled=true,
// isErr=true) — does NOT fall through, because the operation IS trigger.
func TestInterceptWorker_TriggerEmptyName(t *testing.T) {
	rt := &fakeRuntime{}
	deps := workerTestDeps{runtime: rt}
	for _, name := range []string{"", "   "} {
		args := `{"operation":"trigger","name":"` + name + `"}`
		handled, body, isErr := callWorker(t, deps, args)
		require.True(t, handled, "trigger op must be handled even on empty name")
		require.True(t, isErr, "empty name must surface an error")
		assert.Contains(t, body, "name is required")
	}
	assert.Empty(t, rt.triggerCalls, "OnManualTrigger must not be called on empty-name reject")
}

// TestInterceptWorker_TriggerRuntimeNil pins the degrade-not-die
// behavior from Phase G: when wireWorkerRuntime fails at boot, the
// runtime is nil and the intercept surfaces an actionable error rather
// than crashing.
func TestInterceptWorker_TriggerRuntimeNil(t *testing.T) {
	deps := workerTestDeps{runtime: nil}
	handled, body, isErr := callWorker(t, deps, `{"operation":"trigger","name":"smoke"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "dream runtime not available")
}

// TestInterceptWorker_TriggerRuntimeError pins that runtime errors are
// forwarded verbatim with a worker:trigger prefix — this is how the
// "no worker named X" / "worker disabled" messages surface to the LLM.
func TestInterceptWorker_TriggerRuntimeError(t *testing.T) {
	rt := &fakeRuntime{triggerErr: assertableError("worker \"ghost\" not found")}
	deps := workerTestDeps{runtime: rt}
	handled, body, isErr := callWorker(t, deps, `{"operation":"trigger","name":"ghost"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "worker:trigger:")
	assert.Contains(t, body, "ghost")
}

// TestInterceptWorker_StatusHappyPath pins that operation=status reads
// the canned []InvocationRecord through the runtime and returns it as
// JSON. Mirrors the server-side handleWorkerStatus output shape so
// downstream consumers don't have to branch on which side handled the
// call.
func TestInterceptWorker_StatusHappyPath(t *testing.T) {
	rt := &fakeRuntime{
		statusRecords: []dream.InvocationRecord{
			{Kind: "end", Status: "ok", DurationMs: 42},
			{Kind: "start", Trigger: "manual"},
		},
	}
	deps := workerTestDeps{runtime: rt}
	handled, body, isErr := callWorker(t, deps, `{"operation":"status","name":"smoke","limit":5}`)
	require.True(t, handled, "status op must be handled client-side")
	require.False(t, isErr, "expected non-error result, got: %s", body)

	var got []dream.InvocationRecord
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "end", got[0].Kind)
	assert.Equal(t, int64(42), got[0].DurationMs)

	require.Len(t, rt.statusCalls, 1)
	assert.Equal(t, "smoke", rt.statusCalls[0].name)
	assert.Equal(t, 5, rt.statusCalls[0].limit)
}

// TestInterceptWorker_StatusDefaultLimit pins that absent / zero limit
// becomes 10 at the runtime boundary — matches the server-side
// handleWorkerStatus which substitutes 10 when the wire value is <= 0.
func TestInterceptWorker_StatusDefaultLimit(t *testing.T) {
	rt := &fakeRuntime{}
	deps := workerTestDeps{runtime: rt}
	handled, _, isErr := callWorker(t, deps, `{"operation":"status","name":"smoke"}`)
	require.True(t, handled)
	require.False(t, isErr)
	require.Len(t, rt.statusCalls, 1)
	assert.Equal(t, 10, rt.statusCalls[0].limit)
}

// TestInterceptWorker_StatusEmptyName pins that operation=status with
// an empty name surfaces an error result rather than falling through.
func TestInterceptWorker_StatusEmptyName(t *testing.T) {
	rt := &fakeRuntime{}
	deps := workerTestDeps{runtime: rt}
	handled, body, isErr := callWorker(t, deps, `{"operation":"status","name":""}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "name is required")
	assert.Empty(t, rt.statusCalls)
}

// TestInterceptWorker_StatusRuntimeNil pins degrade-not-die on the
// status path symmetric to the trigger path.
func TestInterceptWorker_StatusRuntimeNil(t *testing.T) {
	deps := workerTestDeps{runtime: nil}
	handled, body, isErr := callWorker(t, deps, `{"operation":"status","name":"smoke"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "dream runtime not available")
}

// TestInterceptWorker_NotReadyGate pins the bind-first wiring-window gate
// (bind-first startup): every worker op that touches the runtime, with
// WorkerReady()=false, returns the uniform "daemon still starting" error
// and does NOT dispatch through the runtime — even when a live runtime is
// present (the readiness check fires BEFORE the nil check and before any
// dispatch). The fails-when-absent guarantee: drop the readiness gate and
// the trigger/status/cancel cases reach the runtime (triggerCalls /
// statusCalls / cancelCalls non-empty), and running returns the live
// (empty) list instead of the not-ready error.
func TestInterceptWorker_NotReadyGate(t *testing.T) {
	for _, tc := range []struct {
		op   string
		args string
	}{
		{"trigger", `{"operation":"trigger","name":"smoke"}`},
		{"status", `{"operation":"status","name":"smoke"}`},
		{"running", `{"operation":"running"}`},
		{"cancel", `{"operation":"cancel","name":"smoke"}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			rt := &fakeRuntime{}
			deps := workerTestDeps{runtime: rt, notReady: true}
			handled, body, isErr := callWorker(t, deps, tc.args)
			require.True(t, handled, "op must be handled client-side")
			require.True(t, isErr, "not-ready op must be an error result")
			assert.Contains(t, body, "daemon still starting")
			assert.Contains(t, body, "worker:"+tc.op)
			// No dispatch reached the runtime.
			assert.Empty(t, rt.triggerCalls, "trigger must not dispatch when not ready")
			assert.Empty(t, rt.statusCalls, "status must not dispatch when not ready")
			assert.Empty(t, rt.cancelCalls, "cancel must not dispatch when not ready")
		})
	}
}

// TestInterceptWorker_ReadyNilRuntimeDegrades pins that with WorkerReady()=true
// but a nil runtime (the genuine permanent boot-degrade case), every runtime
// op falls past the readiness gate and surfaces the existing "not available"
// degrade message — NOT the "daemon still starting" wiring-window message.
func TestInterceptWorker_ReadyNilRuntimeDegrades(t *testing.T) {
	for _, tc := range []struct {
		op   string
		args string
	}{
		{"trigger", `{"operation":"trigger","name":"smoke"}`},
		{"status", `{"operation":"status","name":"smoke"}`},
		{"running", `{"operation":"running"}`},
		{"cancel", `{"operation":"cancel","name":"smoke"}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			deps := workerTestDeps{runtime: nil} // notReady false → WorkerReady()=true
			handled, body, isErr := callWorker(t, deps, tc.args)
			require.True(t, handled)
			require.True(t, isErr)
			assert.Contains(t, body, "dream runtime not available")
			assert.NotContains(t, body, "daemon still starting")
		})
	}
}

// TestInterceptWorker_UnknownOpHandledClientSide pins the
// behavior: every worker op is now intercepted client-side, so unknown
// operations surface here rather than reaching the server (which has
// no worker handler). The error message lists every valid
// operation so a misspelled call is self-diagnosing.
func TestInterceptWorker_UnknownOpHandledClientSide(t *testing.T) {
	deps := workerTestDeps{runtime: &fakeRuntime{}, crud: &fakeCRUD{}}
	params := kgtools.CallToolParams{
		Name:      "worker",
		Arguments: json.RawMessage(`{"operation":"banana"}`),
	}
	handled, res := InterceptWorker(deps, params)
	require.True(t, handled, "unknown op must be handled client-side")
	require.True(t, res.IsError)
	require.NotEmpty(t, res.Content)
	body := res.Content[0].Text
	assert.Contains(t, body, "unknown operation")
	assert.Contains(t, body, "banana")
	for _, op := range []string{"list", "create", "update", "delete", "trigger", "status", "running", "cancel"} {
		assert.Contains(t, body, op, "error message must list every valid operation")
	}
}

// TestInterceptWorker_MalformedArgs pins that JSON parse failures
// surface here (handled=true, isErr=true) rather than getting forwarded
// to the server. Mirrors the prologue unmarshal-or-error pattern in
// InterceptAst.
func TestInterceptWorker_MalformedArgs(t *testing.T) {
	deps := workerTestDeps{runtime: &fakeRuntime{}}
	params := kgtools.CallToolParams{
		Name:      "worker",
		Arguments: json.RawMessage(`{"operation":}`), // syntactically invalid
	}
	handled, res := InterceptWorker(deps, params)
	require.True(t, handled, "malformed args must be handled, not fall through")
	require.True(t, res.IsError)
	require.NotEmpty(t, res.Content)
	assert.Contains(t, res.Content[0].Text, "invalid arguments")
}

// assertableError is a tiny error type for runtime-error tests — keeps
// test fixtures inline without importing errors / fmt for one-shot
// values.
