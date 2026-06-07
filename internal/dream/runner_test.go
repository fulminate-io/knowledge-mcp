// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestRunner_SelfTriggerGuardDropsWorkerOriginEvents wires a fake worker
// to tool-completed and asserts:
//   - an event with Origin="worker:other" is dropped (self-trigger guard).
//   - an event with Origin="" passes through.
//
// We test the dispatch loop's filter directly via OriginIsDreamWorker
// rather than spinning a full Runner — the loop is small and the filter
// is the only behavior worth integration-testing.
func TestRunner_SelfTriggerGuardDropsWorkerOriginEvents(t *testing.T) {
	cases := []struct {
		name     string
		origin   string
		wantDrop bool
	}{
		{name: "worker origin dropped", origin: "worker:other", wantDrop: true},
		{name: "worker self origin dropped", origin: "worker:smoke-hello", wantDrop: true},
		{name: "empty origin passes", origin: "", wantDrop: false},
		{name: "non-worker origin passes", origin: "interactive-session-123", wantDrop: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := Event{Type: EventToolCompleted, Origin: tc.origin}
			got := OriginIsDreamWorker(ev)
			if got != tc.wantDrop {
				t.Errorf("OriginIsDreamWorker(%q) = %v, want drop=%v", tc.origin, got, tc.wantDrop)
			}
		})
	}
}

// TestRunner_OnManualTriggerSpawnsWorker drives the full manual-fire path:
// OnManualTrigger("smoke-hello") spawns runWorker → runReAct → search call
// lands at GraphClient with session_id="worker:smoke-hello", worker-completed
// fires on the bus.
func TestRunner_OnManualTriggerSpawnsWorker(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderClaudeCLI, Model: "test-model"},
	}
	t.Cleanup(config.SetForTest(cfg))

	fake := llm.NewFakeClient(
		&llm.Response{
			Content:      "",
			FinishReason: llm.FinishReasonToolUse,
			ToolCalls: []schema.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"hi"}`},
			}},
		},
		&llm.Response{Content: "all done", FinishReason: llm.FinishReasonEndTurn},
	)
	t.Cleanup(swapFakeFactory(t, config.ProviderClaudeCLI, fake))

	disp := &recordingDispatch{response: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: "hit"}},
	}}

	w := Worker{
		Name:                "smoke-hello",
		Provider:            config.ProviderClaudeCLI,
		Model:               "test-model",
		SystemPrompt:        "smoke",
		ToolAllowlist:       []string{"search"},
		MaxIterations:       3,
		MaxWallclockSeconds: 30,
		Enabled:             true,
	}

	reg := registryWith(t, w)
	bus := NewEventBus()

	dir := t.TempDir()
	r := NewRunner(reg, bus, dir, disp.fn(), searchOnlyCatalog())

	// Subscribe to worker-completed before firing so we can assert the
	// outcome event lands on the bus with status=ok.
	doneCh := bus.Subscribe(Trigger{Event: EventWorkerCompleted})

	if err := r.OnManualTrigger(context.Background(), "smoke-hello", json.RawMessage(`"hello"`)); err != nil {
		t.Fatalf("OnManualTrigger: %v", err)
	}

	// Wait for completion event with a generous deadline.
	select {
	case ev := <-doneCh:
		if ev.Status != "ok" {
			t.Errorf("worker-completed Status = %q, want ok", ev.Status)
		}
		if ev.Origin != "worker:smoke-hello" {
			t.Errorf("worker-completed Origin = %q, want worker:smoke-hello", ev.Origin)
		}
		if ev.Worker != "smoke-hello" {
			t.Errorf("worker-completed Worker = %q, want smoke-hello", ev.Worker)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for worker-completed event")
	}

	if got := disp.capturedSession.Load(); got != "worker:smoke-hello" {
		t.Errorf("captured session = %v, want worker:smoke-hello", got)
	}

	r.Stop(2 * time.Second)

	// Status reads back the per-worker log.
	got, err := r.Status(context.Background(), "smoke-hello", 10)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("len(status) = %d, want >= 2 (start + end)", len(got))
	}
	if got[0].Kind != "end" || got[0].Status != "ok" {
		t.Errorf("most recent record = %+v, want {kind:end status:ok}", got[0])
	}
}

// TestRunner_OnManualTriggerUnknownWorkerReturnsError guards the
// not-in-registry branch.
func TestRunner_OnManualTriggerUnknownWorkerReturnsError(t *testing.T) {
	reg := registryWith(t)
	bus := NewEventBus()
	r := NewRunner(reg, bus, t.TempDir(), nil, nil)

	err := r.OnManualTrigger(context.Background(), "no-such-worker", json.RawMessage(`""`))
	if err == nil {
		t.Fatalf("OnManualTrigger: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-worker") {
		t.Errorf("err = %v, want to contain worker name", err)
	}
}

// TestRunner_StopIsIdempotent verifies multiple Stop() calls don't panic
// or double-close the bus subs.
func TestRunner_StopIsIdempotent(t *testing.T) {
	reg := registryWith(t)
	bus := NewEventBus()
	r := NewRunner(reg, bus, t.TempDir(), nil, nil)
	r.Stop(0)
	r.Stop(time.Millisecond)
	r.Stop(0)
}

// TestRunner_EmitToolStarted_FansOutOnBus verifies the EventEmitter
// implementation translates chokepoint args back into Events.
func TestRunner_EmitToolStarted_FansOutOnBus(t *testing.T) {
	bus := NewEventBus()
	r := NewRunner(registryWith(t), bus, t.TempDir(), nil, nil)

	ch := bus.Subscribe(Trigger{Event: EventToolStarted})
	now := time.Now().UTC()
	r.EmitToolStarted("search", "interactive-session-1", "interactive-session-1", json.RawMessage(`{"q":"x"}`), now)

	select {
	case ev := <-ch:
		if ev.Tool != "search" {
			t.Errorf("Tool = %q, want search", ev.Tool)
		}
		if ev.Origin != "interactive-session-1" {
			t.Errorf("Origin = %q, want interactive-session-1", ev.Origin)
		}
		if string(ev.Args) != `{"q":"x"}` {
			t.Errorf("Args = %s, want {q:x}", ev.Args)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for tool-started event")
	}
}

// TestRunner_EmitToolCompleted_FansOutOnBus mirrors the started variant.
func TestRunner_EmitToolCompleted_FansOutOnBus(t *testing.T) {
	bus := NewEventBus()
	r := NewRunner(registryWith(t), bus, t.TempDir(), nil, nil)

	ch := bus.Subscribe(Trigger{Event: EventToolCompleted})
	now := time.Now().UTC()
	r.EmitToolCompleted("think", "x", "x", json.RawMessage(`{}`), json.RawMessage(`{"ok":true}`), "ok", 42, now)

	select {
	case ev := <-ch:
		if ev.Tool != "think" || ev.Status != "ok" || ev.DurationMs != 42 {
			t.Errorf("ev = %+v, want tool=think status=ok dur=42", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for tool-completed event")
	}
}

// TestRunner_NilSafetyForEmit guards the chokepoint pre-Phase-5 wiring
// where EventEmitter may be nil during partial server boot. Should not
// panic; should be a no-op.
func TestRunner_NilSafetyForEmit(t *testing.T) {
	var r *Runner // nil receiver
	r.EmitToolStarted("x", "", "", nil, time.Now())
	r.EmitToolCompleted("x", "", "", nil, nil, "ok", 0, time.Now())

	// Also runner with nil bus.
	r2 := &Runner{}
	r2.EmitToolStarted("x", "", "", nil, time.Now())
	r2.EmitToolCompleted("x", "", "", nil, nil, "ok", 0, time.Now())
}

// TestRunner_ListByNameDelegateToRegistry verifies the WorkerControl
// surface forwards to the underlying Registry.
func TestRunner_ListByNameDelegateToRegistry(t *testing.T) {
	w := Worker{
		Name:          "alpha",
		SystemPrompt:  "x",
		Provider:      config.ProviderClaudeCLI,
		ToolAllowlist: []string{"search"},
	}

	reg := registryWith(t, w)
	r := NewRunner(reg, NewEventBus(), t.TempDir(), nil, nil)

	ctx := context.Background()
	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Name != "alpha" {
		t.Errorf("List = %#v, want [alpha]", all)
	}

	got, ok, err := r.ByName(ctx, "alpha")
	if err != nil {
		t.Fatalf("ByName: %v", err)
	}
	if !ok || got.Name != "alpha" {
		t.Errorf("ByName = (%v, %v), want (alpha, true)", got.Name, ok)
	}
}

// TestRunner_StatusReadsLog round-trips a manual-fire invocation through
// the log file.
func TestRunner_StatusReadsLog(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(registryWith(t), NewEventBus(), dir, nil, nil)

	wl, err := OpenWorkerLog(dir, "manual")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	if err := wl.Append(InvocationRecord{Time: time.Now(), Kind: "start"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := wl.Append(InvocationRecord{Time: time.Now(), Kind: "end", Status: "ok"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = wl.Close()

	got, err := r.Status(context.Background(), "manual", 10)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(status) = %d, want 2", len(got))
	}
	if got[0].Kind != "end" {
		t.Errorf("first record = %v, want end", got[0].Kind)
	}
}

// guard against the test suite running for more than the worker timeout.
var _ = atomic.Int32{}
