// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// searchOnlyCatalog is the minimal client-owned catalog the runner tests wire
// onto the Runner. T-GTB4: BuildAllowedTools filters this value by the worker's
// allowlist (no GetToolSchemas RPC); the worker's actual tool call rides
// r.Dispatch (the injected DispatchFunc — in production the intercept-chain →
// engine.Dispatch composer; here a recording in-test fake).
func searchOnlyCatalog() []kgtools.MCPTool {
	return []kgtools.MCPTool{
		{Name: "search", Description: "find code", InputSchema: kgtools.InputSchema{
			Type:       "object",
			Properties: map[string]kgtools.Property{"q": {Type: "string"}},
		}},
	}
}

// recordingDispatch is an in-test DispatchFunc that captures the tool name +
// the worker session_id mcpTool.InvokableRun stamps onto ctx (via
// session.ContextWithSessionID). T-GTB4: the worker no longer routes through a
// ToolService.Call wire — the session rides ctx to the DispatchFunc directly, so
// the fake reads it locally instead of asserting a wire round-trip.
type recordingDispatch struct {
	capturedSession atomic.Value // string
	capturedTool    atomic.Value // string
	response        kgtools.ToolResult
}

func (d *recordingDispatch) fn() DispatchFunc {
	return func(ctx context.Context, name string, _ json.RawMessage) (kgtools.ToolResult, error) {
		d.capturedSession.Store(session.SessionIDFromContext(ctx))
		d.capturedTool.Store(name)
		return d.response, nil
	}
}

// TestRunReAct_FullLoopStampsSessionAndCompletes is the end-to-end coverage
// for Step 4: a Worker with two ReAct turns (one tool-use, one final
// response) runs to completion, the tool call lands at the GraphClient with
// session_id="worker:<name>", and the WorkerLog records start + end.
func TestRunReAct_FullLoopStampsSessionAndCompletes(t *testing.T) {
	// Install a deterministic config so resolveDreamSection has something
	// to return. ProviderClaudeCLI keeps the Validate path simple (no API
	// key required) and the FakeClient factory swap works the same.
	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderClaudeCLI, Model: "test-model"},
	}
	t.Cleanup(config.SetForTest(cfg))

	// Stub the Anthropic factory to hand out a FakeClient whose response
	// queue scripts the ReAct loop: turn 1 emits a tool_use; turn 2 emits
	// a final assistant message (no tool calls).
	fake := llm.NewFakeClient(
		&llm.Response{
			Content:      "",
			FinishReason: llm.FinishReasonToolUse,
			ToolCalls: []schema.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"hello"}`},
			}},
		},
		&llm.Response{
			Content:      "all done",
			FinishReason: llm.FinishReasonEndTurn,
		},
	)
	cleanup := swapFakeFactory(t, config.ProviderClaudeCLI, fake)
	t.Cleanup(cleanup)

	// Recording DispatchFunc captures the tool name + the worker session_id
	// stamped on ctx by mcpTool.InvokableRun.
	disp := &recordingDispatch{response: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: "hit one"}},
	}}

	dir := t.TempDir()
	r := &Runner{GraphStorage: dir, Dispatch: disp.fn(), Catalog: searchOnlyCatalog()}
	w := Worker{
		Name:                "smoke-hello",
		Provider:            config.ProviderClaudeCLI,
		Model:               "test-model",
		SystemPrompt:        "You are a smoke worker.",
		ToolAllowlist:       []string{"search"},
		MaxIterations:       3,
		MaxWallclockSeconds: 30,
		Enabled:             true,
	}

	log, err := OpenWorkerLog(dir, w.Name)
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	defer func() { _ = log.Close() }()

	if err := r.runReAct(context.Background(), w, "hi", log, "test-inv-id"); err != nil {
		t.Fatalf("runReAct: %v", err)
	}
	_ = log.Close()

	// The DispatchFunc sees session_id="worker:<name>" stamped on ctx + the tool name.
	if got := disp.capturedSession.Load(); got != "worker:smoke-hello" {
		t.Errorf("captured session = %v, want worker:smoke-hello", got)
	}
	if got := disp.capturedTool.Load(); got != "search" {
		t.Errorf("captured tool = %v, want search", got)
	}

	// Two LLM calls fired (tool-use turn + final turn).
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("FakeClient calls = %d, want 2", len(calls))
	}

	// Log carries start + end records, end with status=ok.
	got, err := ReadRecent(dir, w.Name, 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReadRecent len = %d, want 2", len(got))
	}
	if got[0].Kind != "end" || got[0].Status != "ok" {
		t.Errorf("end record = %+v, want kind=end status=ok", got[0])
	}
	if got[1].Kind != "start" {
		t.Errorf("start record = %+v, want kind=start", got[1])
	}
	if !strings.Contains(string(got[0].Result), "all done") {
		t.Errorf("end Result = %q, want to contain 'all done'", got[0].Result)
	}
}

// TestRunReAct_LLMErrorPropagates verifies a Generate failure surfaces as
// an error AND lands on the log with status=error.
func TestRunReAct_LLMErrorPropagates(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderClaudeCLI, Model: "test-model"},
	}
	t.Cleanup(config.SetForTest(cfg))

	fake := llm.NewFakeClient()
	fake.SetError(errFakeBoom)
	t.Cleanup(swapFakeFactory(t, config.ProviderClaudeCLI, fake))

	// The LLM errors before any tool dispatch, so a no-op recording DispatchFunc
	// is all the Runner needs.
	disp := &recordingDispatch{}

	dir := t.TempDir()
	r := &Runner{GraphStorage: dir, Dispatch: disp.fn(), Catalog: searchOnlyCatalog()}
	w := Worker{
		Name:                "boom",
		Provider:            config.ProviderClaudeCLI,
		Model:               "test-model",
		SystemPrompt:        "boom",
		ToolAllowlist:       []string{"search"},
		MaxIterations:       1,
		MaxWallclockSeconds: 5,
	}

	log, err := OpenWorkerLog(dir, w.Name)
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	defer func() { _ = log.Close() }()

	err = r.runReAct(context.Background(), w, "hi", log, "test-inv-id")
	if err == nil {
		t.Fatalf("runReAct: want error, got nil")
	}
	_ = log.Close()

	got, err := ReadRecent(dir, w.Name, 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	// Find the end record; verify status=error.
	var endRec *InvocationRecord
	for i := range got {
		if got[i].Kind == "end" {
			endRec = &got[i]
			break
		}
	}
	if endRec == nil {
		t.Fatalf("no end record in log: %+v", got)
	}
	if endRec.Status != "error" {
		t.Errorf("end status = %q, want error", endRec.Status)
	}
}

// TestResolveDreamSection_WorkerOverridesConfig verifies Worker.Provider /
// Worker.Model take precedence over the [dream] section.
func TestResolveDreamSection_WorkerOverridesConfig(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderAnthropic, Model: "default-model"},
		Dream:   &config.Section{Provider: config.ProviderOpenAI, Model: "dream-model"},
	}
	t.Cleanup(config.SetForTest(cfg))

	// No worker override → resolves to dream section.
	provider, model, _, err := resolveDreamSection(Worker{Name: "x"})
	if err != nil {
		t.Fatalf("resolveDreamSection: %v", err)
	}
	if provider != config.ProviderOpenAI || model != "dream-model" {
		t.Errorf("(%v, %v), want (openai, dream-model)", provider, model)
	}

	// Worker override → wins.
	provider, model, _, err = resolveDreamSection(Worker{Name: "y", Provider: config.ProviderGemini, Model: "gemini-pro"})
	if err != nil {
		t.Fatalf("resolveDreamSection: %v", err)
	}
	if provider != config.ProviderGemini || model != "gemini-pro" {
		t.Errorf("(%v, %v), want (gemini, gemini-pro)", provider, model)
	}
}

// ---- test helpers ----

var errFakeBoom = errFake("provider exploded")

type errFake string

func (e errFake) Error() string { return string(e) }

// swapFakeFactory installs a factory that returns the shared fake under
// the given provider id, restoring whatever was there before on cleanup.
//
// The fake is shared across all NewClient calls — runReAct invokes
// llm.NewClient once per invocation, but tests script the response queue
// up-front so the order of construction doesn't matter.
func swapFakeFactory(t *testing.T, p config.Provider, fake *llm.FakeClient) func() {
	t.Helper()
	prior := llm.HasProvider(p)
	llm.RegisterProvider(p, func(_ context.Context, _ *llm.Config) (llm.Client, error) {
		return fake, nil
	})
	// We can't snapshot the prior factory without test-only API; the
	// init()-time registration of every provider sub-package re-registers
	// real factories on subsequent test binary loads. Within one binary,
	// we install ours and leave it — subsequent tests in this file
	// install their own swap.
	_ = prior
	return func() { /* leave the swap in place; binary-level cleanup */ }
}
