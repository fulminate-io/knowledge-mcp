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
// onto the Runner. BuildAllowedTools filters this value by the worker's
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
// session.ContextWithSessionID). The worker no longer routes through a
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

// sequencedDispatch is an in-test DispatchFunc that returns a different
// ToolResult per call, indexed by an atomically-incremented call counter. The
// last response repeats if dispatch fires more times than responses are
// scripted. Local to TestRunReAct_RecoversFromToolError: it lets that test hand
// back an IsError result on call 1 (the error the ReAct loop must recover from)
// and a success on call 2, without disturbing recordingDispatch's fixed-response
// users. All returns carry a nil Go error — the IsError signal rides the
// ToolResult, exactly as the real dispatch chain delivers it.
type sequencedDispatch struct {
	responses []kgtools.ToolResult
	calls     atomic.Int64
}

func (d *sequencedDispatch) fn() DispatchFunc {
	return func(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
		i := int(d.calls.Add(1)) - 1
		if i >= len(d.responses) {
			i = len(d.responses) - 1
		}
		return d.responses[i], nil
	}
}

// count returns how many times the DispatchFunc fired.
func (d *sequencedDispatch) count() int64 { return d.calls.Load() }

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

// TestRunReAct_RecoversFromToolError is the LOOP-level proof of the Bug A fix:
// when a tool dispatch returns IsError, mcpTool.InvokableRun
// hands the error text back to eino as a (string, nil) observation instead of a
// Go error, so the ReAct loop CONTINUES and the model self-corrects. The
// leaf-level tests (TestMcpTool_InvokableRunIsErrorReturnsError / …RPCError) only
// prove InvokableRun returns (string, nil); they do NOT prove agent.Generate keeps
// looping after a tool error. This test closes that gap by scripting a 3-turn loop
// where turn 2 only fires if the turn-1 tool error was fed back as an observation.
//
// Before the fix, the IsError branch returned a Go error → eino NodeRunError →
// runReAct returns non-nil and the loop aborts after the first tool call.
// Assertion 1 (runReAct returns nil) is the assertion that would have failed then;
// assertions 2-4 prove the loop actually continued past the error.
func TestRunReAct_RecoversFromToolError(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderClaudeCLI, Model: "test-model"},
	}
	t.Cleanup(config.SetForTest(cfg))

	// Script a 3-turn loop:
	//   turn 1: tool_use search       (model asks for the tool)
	//   turn 2: tool_use search again (model RETRIES after seeing the error
	//           observation — only reachable if the error became an observation)
	//   turn 3: final assistant message "recovered" (no tool calls)
	fake := llm.NewFakeClient(
		&llm.Response{
			FinishReason: llm.FinishReasonToolUse,
			ToolCalls: []schema.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"bad"}`},
			}},
		},
		&llm.Response{
			FinishReason: llm.FinishReasonToolUse,
			ToolCalls: []schema.ToolCall{{
				ID:       "call_2",
				Type:     "function",
				Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"retry"}`},
			}},
		},
		&llm.Response{
			Content:      "recovered",
			FinishReason: llm.FinishReasonEndTurn,
		},
	)
	t.Cleanup(swapFakeFactory(t, config.ProviderClaudeCLI, fake))

	// Sequenced DispatchFunc: call 1 returns IsError ("bad pattern") — the error
	// the model must recover from; call 2 returns a successful result ("hit one").
	disp := &sequencedDispatch{responses: []kgtools.ToolResult{
		{IsError: true, Content: []kgtools.ContentBlock{{Type: "text", Text: "bad pattern"}}},
		{Content: []kgtools.ContentBlock{{Type: "text", Text: "hit one"}}},
	}}

	dir := t.TempDir()
	r := &Runner{GraphStorage: dir, Dispatch: disp.fn(), Catalog: searchOnlyCatalog()}
	w := Worker{
		Name:                "recover",
		Provider:            config.ProviderClaudeCLI,
		Model:               "test-model",
		SystemPrompt:        "You are a recovering worker.",
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

	// Assertion 1 (NON-NEGOTIABLE): runReAct returns nil — the loop recovered
	// from the tool error instead of aborting. This is the assertion that would
	// have failed before the fix.
	if err := r.runReAct(context.Background(), w, "hi", log, "test-inv-id"); err != nil {
		t.Fatalf("runReAct: want nil (loop must recover from tool error), got %v", err)
	}
	_ = log.Close()

	// Assertion 2: the loop continued past the tool error — exactly 3 model
	// turns fired and exactly 2 tool dispatches fired. Turn 2 + dispatch 2 are
	// only reachable if the error from dispatch 1 was fed back and the loop kept
	// going.
	if calls := fake.Calls(); len(calls) != 3 {
		t.Errorf("FakeClient calls = %d, want 3", len(calls))
	}
	if got := disp.count(); got != 2 {
		t.Errorf("dispatch calls = %d, want 2", got)
	}

	// Assertion 3: the WorkerLog end record is status=ok with the final
	// "recovered" content.
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
	if !strings.Contains(string(got[0].Result), "recovered") {
		t.Errorf("end Result = %q, want to contain 'recovered'", got[0].Result)
	}

	// Assertion 4 (STRONGEST): prove the error was fed back AS AN OBSERVATION,
	// not swallowed. FakeClient captures the messages passed to each Generate
	// call, so turn 2's input (calls[1].Messages) must contain a schema.Tool-role
	// message carrying the error observation text. The observation format is
	// "Error: mcp tool search failed: bad pattern" (mcp_tool.go InvokableRun).
	calls := fake.Calls()
	var toolObs string
	for _, m := range calls[1].Messages {
		if m.Role == schema.Tool {
			toolObs = m.Content
			break
		}
	}
	if toolObs == "" {
		t.Fatalf("turn 2 input had no schema.Tool observation message; messages=%+v", calls[1].Messages)
	}
	if !strings.Contains(toolObs, "Error:") || !strings.Contains(toolObs, "bad pattern") {
		t.Errorf("tool observation = %q, want to contain 'Error:' and 'bad pattern'", toolObs)
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
//
// The provider parameter is kept general on purpose: every caller currently
// passes ProviderClaudeCLI because it is the only provider whose Validate path
// needs no API key, but the helper must register the fake under whatever
// provider the test resolves. nolint:unparam — the constant-arg flag is a
// false positive on a deliberately reusable test seam.
//
//nolint:unparam // provider kept general; all callers share the no-key ClaudeCLI provider today
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
