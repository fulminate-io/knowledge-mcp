// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// runReAct executes ONE worker invocation through the SOLE runtime path:
// in-Go eino ReAct over a substrate-level llm.Client. There is no Provider
// branching at this layer — the substrate's llm.NewClient handles factory
// dispatch, and CLI substrates fail at the first MCP tool call with a
// clear *llm.LLMError ("tools not supported in -p prompt mode").
//
// Lifecycle:
//  1. Resolve [dream] section from config; let Worker.Provider/Model
//     override per-worker when set.
//  2. Build llm.Client via the registered factory (BYOK env-var key).
//  3. Build llmAdapter (eino ToolCallingChatModel) over the client.
//  4. Build []einotool.InvokableTool from the worker's allowlist via
//     BuildAllowedTools, stamping session_id="worker:<name>".
//  5. Construct react.Agent with MaxStep = MaxIterations*2 + 1 (each
//     ReAct step is either a tool-call or a response).
//  6. Compose messages: [system: w.SystemPrompt, user: userPrompt].
//  7. agent.Generate; log each turn via WorkerLog (intermediate calls
//     are observable via the chokepoint event stream — runReAct logs
//     only the start/end framing here).
//
// Wall-clock cap: context.WithTimeout(ctx, w.MaxWallclockSeconds*time.Second)
// applied around the agent.Generate call. The eino MaxStep cap is the
// turn-count safety net; the wallclock cap is the runaway-LLM safety net.
func (r *Runner) runReAct(ctx context.Context, w Worker, userPrompt string, log *WorkerLog, invocationID string) error {
	provider, model, cliBin, baseURL, err := resolveDreamSection(w)
	if err != nil {
		return fmt.Errorf("dream: runReAct: resolve config: %w", err)
	}

	apiKey := config.APIKeyForProvider(provider)
	client, err := llm.NewClient(ctx, &llm.Config{
		Provider: provider,
		Model:    llm.Model(model),
		APIKey:   apiKey,
		BaseURL:  baseURL,
		CLIBin:   cliBin,
	})
	if err != nil {
		return fmt.Errorf("dream: runReAct: llm.NewClient: %w", err)
	}

	// Dream workers operate INSIDE the project context — claude-cli's
	// `.mcp.json` auto-detection is part of the worker's tool surface,
	// and the API providers ignore InheritWorkdir. Summarizer + the
	// startup precheck stay in os.TempDir() (the default).
	chatModel := newLLMAdapter(client, llm.Model(model), llm.WithInheritWorkdir())

	tools, err := BuildAllowedTools(r.Catalog, w.ToolAllowlist, "worker:"+w.Name, r.Dispatch)
	if err != nil {
		return fmt.Errorf("dream: runReAct: BuildAllowedTools: %w", err)
	}

	maxIter := w.MaxIterations
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations()
	}
	maxWall := w.MaxWallclockSeconds
	if maxWall <= 0 {
		maxWall = DefaultMaxWallclockSeconds()
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               einoBaseToolsFromInvokable(tools),
			ToolCallMiddlewares: []compose.ToolMiddleware{loggingToolMiddleware(w.Name)},
		},
		MaxStep: maxIter*2 + 1,
	})
	if err != nil {
		return fmt.Errorf("dream: runReAct: react.NewAgent: %w", err)
	}

	msgs := []*schema.Message{
		{Role: schema.System, Content: w.SystemPrompt},
		{Role: schema.User, Content: userPrompt},
	}

	timeout := time.Duration(maxWall) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logStart(log, w, userPrompt, invocationID)
	start := time.Now()
	result, err := agent.Generate(runCtx, msgs)
	dur := time.Since(start).Milliseconds()
	if err != nil {
		logEnd(log, "error", dur, err.Error(), invocationID)
		return fmt.Errorf("dream: runReAct: agent.Generate: %w", err)
	}
	logEndWithResult(log, "ok", dur, result, invocationID)
	return nil
}

// resolveDreamSection picks the (provider, model, cliBin, baseURL) tuple to
// drive this invocation. Worker.Provider overrides the [dream] section's
// Provider when set; Worker.Model does the same for Model; Worker.BaseURL
// does the same for the resolved section's base_url (precedence: Worker >
// section > [default]). When config is unloaded (test scenarios that
// exercise other code paths) the function returns a clear error rather than
// panicking — the production bootstrap has already loaded config before any
// worker fires.
func resolveDreamSection(w Worker) (config.Provider, string, string, string, error) {
	if !config.Loaded() {
		// Workers running before config loads is a real bug, not a
		// degraded-not-die scenario — surface it.
		return "", "", "", "", errors.New("config.LoadOrAutoDetect() not called")
	}
	sec, err := config.Active().Resolve(config.ConsumerDream)
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve dream config: %w", err)
	}
	provider := sec.Provider
	if w.Provider != "" {
		provider = w.Provider
	}
	model := sec.Model
	if w.Model != "" {
		model = w.Model
	}
	baseURL := sec.BaseURL
	if w.BaseURL != "" {
		baseURL = w.BaseURL
	}
	if provider == "" {
		return "", "", "", "", errors.New("no LLM provider resolved for worker")
	}
	if model == "" {
		return "", "", "", "", errors.New("no LLM model resolved for worker")
	}
	return provider, model, sec.CLIBin, baseURL, nil
}

// einoBaseToolsFromInvokable upcasts InvokableTool → BaseTool for eino's
// ToolsNodeConfig.Tools field. compose.ToolsNodeConfig accepts BaseTool;
// the InvokableTool subtype is resolved at call time when ToolsNode dispatches.
func einoBaseToolsFromInvokable(tools []einotool.InvokableTool) []einotool.BaseTool {
	out := make([]einotool.BaseTool, len(tools))
	for i, t := range tools {
		out[i] = t
	}
	return out
}

// loggingToolMiddleware returns a compose.ToolMiddleware that emits an
// info-level audit record for every tool call eino dispatches on the
// worker's behalf. One line per call captures the operator-facing
// signal — which tool fired, with what arg shape, how long it took, did
// it succeed — without dumping the full tool output (already retrievable
// via the worker invocation log + graph mutations). The args summary is
// truncated to argsPreview runes so the line stays readable.
//
// Errors are logged at warn level so a single ERROR line stands out in
// the operator's stream when a tool dispatch fails.
func loggingToolMiddleware(workerName string) compose.ToolMiddleware {
	const argsPreview = 120
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				start := time.Now()
				out, callErr := next(ctx, input)
				dur := time.Since(start).Milliseconds()
				if callErr != nil {
					slog.Warn("dream: tool call failed",
						"worker", workerName,
						"tool", input.Name,
						"args", truncate(input.Arguments, argsPreview),
						"duration_ms", dur,
						"error", callErr.Error(),
					)
					return out, callErr
				}
				resultBytes := 0
				if out != nil {
					resultBytes = len(out.Result)
				}
				slog.Info("dream: tool call",
					"worker", workerName,
					"tool", input.Name,
					"args", truncate(input.Arguments, argsPreview),
					"duration_ms", dur,
					"result_bytes", resultBytes,
				)
				return out, callErr
			}
		},
	}
}

// truncate caps a string at n runes; longer strings get an ellipsis suffix.
// Used by the logging middleware to keep tool-call log lines readable
// while preserving enough of the args shape to recognize the call.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// logStart emits the first record of an invocation. Best-effort — log
// errors are swallowed so a faulty log writer doesn't abort the run.
// invocationID is the per-run UUID so cancel-by-id can target this run.
func logStart(log *WorkerLog, w Worker, userPrompt string, invocationID string) {
	if log == nil {
		return
	}
	// Marshaling a struct of primitive-typed fields can't return an error
	// in practice; on an impossible failure we drop Args rather than
	// abort the run.
	args, err := json.Marshal(struct {
		Worker      string `json:"worker"`
		UserPrompt  string `json:"user_prompt"`
		MaxIter     int    `json:"max_iterations"`
		MaxWallSecs int    `json:"max_wallclock_seconds"`
	}{
		Worker:      w.Name,
		UserPrompt:  userPrompt,
		MaxIter:     w.MaxIterations,
		MaxWallSecs: w.MaxWallclockSeconds,
	})
	if err != nil {
		args = nil
	}
	_ = log.Append(InvocationRecord{
		Time:         time.Now().UTC(),
		InvocationID: invocationID,
		Kind:         "start",
		Trigger:      "manual",
		Args:         args,
	})
}

// logEnd emits the last record of an invocation when the result is an
// error. Best-effort — see logStart.
func logEnd(log *WorkerLog, status string, durMs int64, errMsg, invocationID string) {
	if log == nil {
		return
	}
	_ = log.Append(InvocationRecord{
		Time:         time.Now().UTC(),
		InvocationID: invocationID,
		Kind:         "end",
		Status:       status,
		DurationMs:   durMs,
		Error:        errMsg,
	})
}

// logEndWithResult emits the last record when the run succeeded. The
// result message's Content goes into the Result field so worker:status
// can render the final output.
func logEndWithResult(log *WorkerLog, status string, durMs int64, result *schema.Message, invocationID string) {
	if log == nil || result == nil {
		logEnd(log, status, durMs, "", invocationID)
		return
	}
	// Single-string struct: Marshal cannot return an error on this shape.
	// Keep the err-check anyway so static analysis is satisfied.
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: result.Content})
	if err != nil {
		body = nil
	}
	_ = log.Append(InvocationRecord{
		Time:         time.Now().UTC(),
		InvocationID: invocationID,
		Kind:         "end",
		Status:       status,
		DurationMs:   durMs,
		Result:       body,
	})
}
