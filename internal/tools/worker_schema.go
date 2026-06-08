// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// WorkerToolDef returns the unified worker tool definition. Modeled on
// ManageToolDef (server-side tools_manage.go) — one tool with an
// `operation` enum instead of six top-level tools, so adding a new
// worker operation is a schema edit + a dispatch case rather than a
// registry append.
//
// Operations:
//
//   - list:    enumerate every graph-loaded worker, with current Enabled
//     state.
//   - create:  register a new graph-resident worker.
//   - update:  edit a worker's mutable fields. Every field except Name
//     is mutable.
//   - delete:  remove a graph-resident worker.
//   - trigger: fire a worker manually. Returns immediately; the worker
//     runs asynchronously inside the dream Runner. Smoke-test
//     path — a real worker is normally driven by its Triggers.
//   - status:  return the last N invocation records for a named worker
//     (default 10). Reads the per-worker log file written by the
//     Runner's WorkerLog.
//   - running: enumerate live in-flight invocations.
//   - cancel:  cancel in-flight invocations (by invocation id or by
//     worker name).
//
// IMPORTANT: workers require a tool-capable LLM provider (anthropic,
// openai, gemini). CLI providers (claude-cli, codex-cli) parse cleanly
// at config time but fail at the first tool call with *llm.LLMError —
// see domains/dream/doc.go for the runtime contract.
//
// Schema-source-of-truth note: this definition is client-side.
// cmd/knowledge.loadSchemas appends WorkerToolDef() to the merged tool set that
// backs the tools/list response.
func WorkerToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "worker",
		Description: "Unified dream-worker management and invocation tool. " +
			"list: enumerate graph-registered workers and current state. " +
			"create: register a new graph-resident worker. " +
			"update: edit a worker's mutable fields. " +
			"delete: remove a graph-registered worker. " +
			"trigger: fire a worker manually with a payload (smoke-test path). Returns immediately; the worker runs in background. " +
			"status: return recent invocation summaries for a named worker. " +
			"Required params by operation (in addition to the always-required operation): " +
			"create requires name + system_prompt + provider + model + a non-empty tool_allowlist; " +
			"update requires name (every field except name is mutable); " +
			"trigger / status / delete require name; cancel requires invocation OR name; " +
			"list / running require nothing further. " +
			"NOTE: workers require a tool-capable LLM provider (anthropic, openai, gemini). " +
			"CLI providers (claude-cli, codex-cli) parse cleanly but fail at the first tool call with *llm.LLMError.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "Operation to perform",
					Enum:        []string{"list", "create", "update", "delete", "trigger", "status", "running", "cancel"},
				},
				"name":          {Type: "string", Description: "Worker name (identity / lookup key — required for create/update/delete/trigger/status; optional for running/cancel)"},
				"invocation":    {Type: "string", Description: "Per-run UUID for cancel: target a specific in-flight invocation. Discover IDs via worker(operation:\"running\") or the invocation_id field on worker:status start records. Either invocation or name is required for cancel; if both supplied, invocation wins."},
				"description":   {Type: "string", Description: "Worker description (used by create/update)"},
				"system_prompt": {Type: "string", Description: "System prompt fed verbatim to the LLM at the start of every run (used by create/update)"},
				"provider":      {Type: "string", Description: "LLM provider: anthropic | openai | gemini | claude-cli | codex-cli (used by create/update). CLI providers cannot drive tool-use — see tool description."},
				"model":         {Type: "string", Description: "Model identifier (provider-specific, used by create/update). Empty falls back to the [dream] section in ~/.knowledge/config."},
				"base_url":      {Type: "string", Description: "Optional LLM endpoint override for this worker (used by create/update); overrides the [dream]/[default] base_url. Ignored for CLI providers."},
				"tool_allowlist": {
					Type:        "array",
					Items:       &kgtools.Property{Type: "string"},
					Description: "Allowed MCP tool names — required and non-empty for create. Used by create/update.",
				},
				"triggers": {
					Type: "array",
					Items: &kgtools.Property{Type: "object", Description: "Trigger entry: {event, filter, schedule}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
						"event":    {Type: "string", Description: "Event that fires the trigger", Enum: []string{"tool-started", "tool-completed", "worker-started", "worker-completed", "cron", "manual"}},
						"filter":   {Type: "object", Description: "AND-of-equality match on event metadata"},
						"schedule": {Type: "string", Description: "Cron expression (only for event=cron)"},
					}},
					Description: "Trigger entries (used by create/update). Each entry: {event, filter, schedule}. Event ∈ {tool-started, tool-completed, worker-started, worker-completed, cron, manual}. Filter is an AND-of-equality match on event metadata; schedule is a cron expression on Event=cron.",
				},
				"max_iterations":        {Type: "integer", Description: "Max ReAct loop iterations per invocation (used by create/update). 0 means use the package default."},
				"max_wallclock_seconds": {Type: "integer", Description: "Max wallclock seconds per invocation (used by create/update). 0 means use the package default."},
				"enabled":               {Type: "boolean", Description: "Whether the worker is enabled (used by create/update)."},
				"payload":               {Type: "object", Description: "User-prompt payload for the trigger operation. Forwarded to the worker's first ReAct turn as JSON."},
				"limit":                 {Type: "integer", Description: "For status: max recent invocations to return (default 10)."},
				"format":                {Type: "string", Description: "Output format: 'text' (default) or 'json'."},
			},
			Required: []string{"operation"},
		},
	}
}

// workerArgs holds parsed arguments for the worker tool. Field naming
// mirrors the schema property keys so json tags are 1:1; flexInt/flexBool
// are used wherever LLMs frequently quote numeric/boolean params.
//
// Payload is a json.RawMessage rather than a typed struct because the
// trigger operation forwards it verbatim to the worker — the runtime
// (not this tool) decides how to interpret it.
type workerArgs struct {
	Operation           string          `json:"operation"`
	Name                string          `json:"name"`
	Invocation          string          `json:"invocation"`
	Description         string          `json:"description"`
	SystemPrompt        string          `json:"system_prompt"`
	Provider            string          `json:"provider"`
	Model               string          `json:"model"`
	BaseURL             string          `json:"base_url"`
	ToolAllowlist       flexStringSlice `json:"tool_allowlist"`
	Triggers            json.RawMessage `json:"triggers"`
	MaxIterations       flexInt         `json:"max_iterations"`
	MaxWallclockSeconds flexInt         `json:"max_wallclock_seconds"`
	Enabled             *flexBool       `json:"enabled"`
	Payload             json.RawMessage `json:"payload"`
	Limit               flexInt         `json:"limit"`
	Format              string          `json:"format"`
}
