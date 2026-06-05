// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// DispatchFunc runs ONE full standard client tool call: the SAME path
// MCPClient.handleMCPToolCall takes (run the client intercept chain; if
// intercepted return its result, else engine.Dispatch the rewritten args
// through the single Execute/Call passthrough). The dream worker's eino tools
// wrap this so worker tool calls ride the IDENTICAL dispatch upstream MCP
// traffic uses — no worker-specific tool plumbing, no second raw-to-legacy
// passthrough. cmd/knowledge wires it to dispatchForRunner;
// the CLI-subcommand callers wire a degraded dispatch that still routes through
// engine.Dispatch (the single passthrough), never a bespoke one.
type DispatchFunc func(ctx context.Context, name string, args json.RawMessage) (kgtools.ToolResult, error)

// mcpTool wraps one MCP tool name behind eino's InvokableTool interface.
// Every InvokableRun call routes through the standard client dispatch (intercept
// chain then engine.Dispatch) with the worker's session_id stamped onto the
// outgoing context so the chokepoint in cmd/knowledge-server's tools_dispatch
// emits Origin="worker:<name>" on tool-started / tool-completed events.
//
// The substrate keeps no per-call cache. mcpTool instances are constructed
// once per worker invocation by BuildAllowedTools and discarded when ReAct
// returns; sharing across goroutines is safe because the only mutable state
// (sessionID, schema, dispatch) is set at construction time.
type mcpTool struct {
	name        string
	description string
	schemaInfo  *schema.ToolInfo
	sessionID   string
	dispatch    DispatchFunc
}

// Compile-time guarantee: mcpTool satisfies eino's InvokableTool. ReAct's
// ToolsNode rejects mismatched implementations at runtime; this fails the
// build instead.
var _ einotool.InvokableTool = (*mcpTool)(nil)

// Info returns the eino schema describing this tool. The pointer is shared
// across calls — eino treats *schema.ToolInfo as read-only.
func (t *mcpTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.schemaInfo, nil
}

// InvokableRun dispatches one tool call through the standard client path
// (t.dispatch = intercept chain then engine.Dispatch) and packs the resulting
// Content blocks into the single string the eino model expects as a tool message.
// The worker session_id is stamped onto the ctx so the chokepoint emits
// Origin="worker:<name>" on the tool-started/completed events.
//
// Error policy: both failure paths return the error text as
// the tool-result observation string with a nil error, mirroring the success
// path, so the eino ReAct loop continues and the model self-corrects on the next
// step instead of the graph run aborting with NodeRunError.
//   - dispatch transport / RPC error → return "Error: mcp tool <name> failed:
//     <err>" as the observation (nil error); res may be zero-valued.
//   - ToolResult{IsError: true} → return "Error: mcp tool <name> failed: <text>"
//     as the observation (nil error), where <text> is the concatenated Content.
//   - Successful ToolResult → concatenate Content text blocks and return.
//
// Worker-construction-time failures (an unknown tool name in BuildAllowedTools)
// stay fatal and are NOT part of this observation contract: the model cannot emit
// such a name, so it is surfaced as a hard error before the loop runs.
func (t *mcpTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	ctx = session.ContextWithSessionID(ctx, t.sessionID)
	res, err := t.dispatch(ctx, t.name, json.RawMessage(argumentsInJSON))
	if err != nil {
		// Deliberate: hand the transport/RPC failure back to the ReAct loop as an
		// observation (nil error) so the model can self-correct instead of the eino
		// graph run aborting with NodeRunError. res may be
		// zero-valued here, so build the observation from err.Error().
		return fmt.Sprintf("Error: mcp tool %s failed: %s", t.name, err.Error()), nil
	}
	if res.IsError {
		// Deliberate: a tool-level/application error (bad ast pattern, missing
		// repo, server-side validation) also becomes an observation so the model
		// retries rather than the whole invocation dying.
		return fmt.Sprintf("Error: mcp tool %s failed: %s", t.name, joinTextBlocks(res.Content)), nil
	}
	return joinTextBlocks(res.Content), nil
}

// joinTextBlocks concatenates the Text fields of every text-type ContentBlock
// in res.Content, ignoring any non-text blocks (the substrate currently emits
// only "text" but the proto allows other types). Blocks separate with a
// single newline so the model sees a coherent multi-line tool output.
func joinTextBlocks(blocks []kgtools.ContentBlock) string {
	var sb strings.Builder
	for i, b := range blocks {
		if b.Type != "" && b.Type != "text" {
			continue
		}
		if i > 0 && sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// BuildAllowedTools filters the client-owned tool catalog by allowlist and
// returns one InvokableTool per allowlist entry, in declaration order. Every
// returned tool stamps workerSessionID onto its outgoing context so the
// chokepoint can emit Origin="worker:<name>" on tool events.
//
// catalog (required) is the client-owned MCP tool catalog
// (tools.AllToolSchemas). The Runner carries it (wired at bootstrap from
// tools.AllToolSchemas, which cannot be imported here directly because tools
// imports dream); BuildAllowedTools just filters the value.
//
// dispatch (required) is the standard client tool-call path each returned tool
// routes through — the SAME intercept-chain → engine.Dispatch sequence upstream
// MCP traffic uses (no worker-specific plumbing).
//
// Errors:
//   - Empty allowlist → empty slice + nil error (caller's responsibility to
//     reject; Worker.Validate already does so but the substrate is defensive).
//   - Allowlist name not in the catalog → wrapped "unknown tool"
//     error. ReAct cannot recover from a misspelled tool name; surfacing it
//     at construction time is faster than letting the model emit one and
//     fail the call.
func BuildAllowedTools(catalog []kgtools.MCPTool, allowlist []string, workerSessionID string, dispatch DispatchFunc) ([]einotool.InvokableTool, error) {
	if dispatch == nil {
		return nil, fmt.Errorf("dream: BuildAllowedTools: nil DispatchFunc")
	}
	if workerSessionID == "" {
		return nil, fmt.Errorf("dream: BuildAllowedTools: empty workerSessionID")
	}
	if len(allowlist) == 0 {
		return []einotool.InvokableTool{}, nil
	}

	byName := make(map[string]kgtools.MCPTool, len(catalog))
	for _, t := range catalog {
		byName[t.Name] = t
	}

	out := make([]einotool.InvokableTool, 0, len(allowlist))
	for _, name := range allowlist {
		t, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("dream: BuildAllowedTools: tool %q not in client catalog", name)
		}
		info, err := mcpToolToToolInfo(t)
		if err != nil {
			return nil, fmt.Errorf("dream: BuildAllowedTools: convert schema for %q: %w", name, err)
		}
		out = append(out, &mcpTool{
			name:        t.Name,
			description: t.Description,
			schemaInfo:  info,
			sessionID:   workerSessionID,
			dispatch:    dispatch,
		})
	}
	return out, nil
}

// mcpToolToToolInfo converts a client-owned kgtools.MCPTool (Name, Description,
// InputSchema as a typed struct) into the eino schema.ToolInfo shape expected
// by ToolCallingChatModel.WithTools. The InputSchema is marshaled to JSON
// bytes and decoded into the local mcpInputSchema tree so the conversion logic
// is shared regardless of how the schema arrived. The eino jsonschema package
// re-uses the standard JSON Schema 2020-12 shape; the catalog emits
// draft-07-compatible bytes that round-trip cleanly when the relevant
// keywords stay common (type, properties, required, items, enum, description).
//
// EINO SPIKE (verified against github.com/cloudwego/eino@v0.8.13, schema/tool.go):
//   - The object-children field on schema.ParameterInfo is SubParams
//     (map[string]*ParameterInfo) — tool.go:93. paramInfoToJSONSchema
//     (tool.go:171-210) renders SubParams into nested Properties + a derived
//     nested Required, so nested child keys DO survive the eino/Path-2 render.
//   - ToJSONSchema does NOT emit additionalProperties, and ParameterInfo has NO
//     MaxLength field. So additionalProperties:false and maxLength ride Path 1
//     (the raw tools/list InputSchema marshal — the strict-Codex consumer) ONLY;
//     Path 2 (worker provider render) omits both. Acceptable: worker providers
//     are not the strict consumer.
func mcpToolToToolInfo(t kgtools.MCPTool) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{
		Name: t.Name,
		Desc: t.Description,
	}
	// A no-properties object schema means a no-args tool — leave ParamsOneOf
	// nil so eino correctly reports it.
	if len(t.InputSchema.Properties) == 0 {
		return info, nil
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("marshal input_schema: %w", err)
	}
	// Decode the server's MCP-style {"type":"object","properties":...} shape
	// directly into the eino ParameterInfo tree. We avoid pulling in the
	// eino-contrib/jsonschema dependency because the substrate's tool
	// schemas are well-bounded: object roots with simple-typed properties.
	var sm mcpInputSchema
	if err := json.Unmarshal(raw, &sm); err != nil {
		return nil, fmt.Errorf("unmarshal input_schema: %w", err)
	}
	if sm.Type != "" && sm.Type != "object" {
		return nil, fmt.Errorf("input_schema root type %q: only \"object\" supported", sm.Type)
	}
	params := make(map[string]*schema.ParameterInfo, len(sm.Properties))
	required := make(map[string]bool, len(sm.Required))
	for _, r := range sm.Required {
		required[r] = true
	}
	for name, prop := range sm.Properties {
		params[name] = mcpPropertyToParameterInfo(prop, required[name])
	}
	info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	return info, nil
}

// mcpInputSchema mirrors domains/tools.InputSchema, decoded from JSON. We
// keep a local copy instead of importing domains/tools so domains/dream
// stays decoupled from the kgtools tool-catalog package.
type mcpInputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]mcpProperty `json:"properties"`
	Required   []string               `json:"required"`
}

// mcpProperty mirrors kgtools.Property. Kept as a local mirror (see
// mcpInputSchema above) so domains/dream stays decoupled from the kgtools
// tool-catalog package. Properties/AdditionalProperties/MaxLength mirror the
// fields kgtools.Property now emits; the json.Unmarshal at mcpToolToToolInfo
// captures the nested object sub-shape so it can recurse into eino's SubParams.
type mcpProperty struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Items       *mcpProperty           `json:"items"`
	Properties  map[string]mcpProperty `json:"properties"`
	Enum        []string               `json:"enum"`
	// AdditionalProperties and MaxLength are decoded for fidelity with the
	// kgtools.Property wire shape, but eino's schema.ParameterInfo expresses
	// NEITHER (see the EINO SPIKE note on mcpToolToToolInfo): both ride Path 1
	// (the raw tools/list InputSchema marshal) only, so neither is forwarded
	// into ParameterInfo below.
	AdditionalProperties *bool `json:"additionalProperties"`
	MaxLength            int   `json:"maxLength"`
}

// mcpPropertyToParameterInfo recursively translates the MCP Property shape
// to eino's ParameterInfo. The substrate's tool schemas use a small subset
// of JSON Schema (string/integer/number/boolean/array/object); unknown
// types pass through verbatim and the chat model surfaces the type to the
// downstream provider.
func mcpPropertyToParameterInfo(prop mcpProperty, required bool) *schema.ParameterInfo {
	pi := &schema.ParameterInfo{
		Type:     schema.DataType(prop.Type),
		Desc:     prop.Description,
		Required: required,
	}
	if len(prop.Enum) > 0 {
		pi.Enum = append(pi.Enum, prop.Enum...)
	}
	if prop.Items != nil {
		pi.ElemInfo = mcpPropertyToParameterInfo(*prop.Items, false)
	}
	// Recurse the nested object sub-shape into eino's SubParams (the confirmed
	// object-children field — see the EINO SPIKE note above), mirroring the
	// ElemInfo recursion. A nested key is Required in eino only when it sits in
	// the nested object's own additionalProperties:false-closed shape; the wire
	// Property does not carry a per-object Required list, so nested children are
	// rendered non-required here (Path 1's raw JSON carries the authoritative
	// closed-object semantics for the strict consumer). prop.MaxLength and
	// prop.AdditionalProperties are intentionally NOT forwarded: ParameterInfo
	// expresses neither, so both ride Path 1 only.
	if len(prop.Properties) > 0 {
		pi.SubParams = make(map[string]*schema.ParameterInfo, len(prop.Properties))
		for name, child := range prop.Properties {
			pi.SubParams[name] = mcpPropertyToParameterInfo(child, false)
		}
	}
	return pi
}
