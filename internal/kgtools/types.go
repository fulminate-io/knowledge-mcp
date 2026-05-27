// SPDX-License-Identifier: Apache-2.0

package kgtools

import (
	"encoding/json"
	"strings"
)

// MCP JSON-RPC types used by tool handlers and dispatch.

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPTool describes a single MCP tool with its schema.
type MCPTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema describes the JSON schema for tool input parameters.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property describes a single JSON schema property.
type Property struct {
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Items       *Property `json:"items,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	// MaxLength is the JSONSchema maxLength bound for string params (0 = unset,
	// omitted). Surfacing the cap structurally — not only in Description prose —
	// gives the model the limit up front, cutting over-length retries on capped
	// fields (e.g. the search-optimized summary, and Linear-synced ticket/project
	// titles whose backend cap is tighter than the generic summary cap).
	MaxLength int `json:"maxLength,omitempty"`
}

// ToolResult is the structured result returned by a tool handler.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single piece of content in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolParams holds the parameters for a tools/call JSON-RPC request.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      *CallToolMeta   `json:"_meta,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// CallToolMeta holds optional metadata for a tool call (e.g., progress token).
type CallToolMeta struct {
	ProgressToken json.RawMessage `json:"progressToken,omitempty"`
}

// TextResult returns a successful tool result with the given text content.
// Invalid UTF-8 bytes are replaced with U+FFFD so the result survives
// protobuf marshaling — the gRPC wire forbids invalid UTF-8 in string
// fields, and tool outputs that interpolate node content (Description,
// summaries, source code) can pick up bad bytes from upstream collectors
// that read non-UTF-8 source files. Without this, assemble / search / any
// content-rendering tool can fail end-to-end on a single bad byte
// somewhere in the graph.
func TextResult(text string) ToolResult {
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: sanitizeUTF8(text)}}}
}

// ErrorResult returns an error tool result with the given message. Same
// UTF-8 sanitization as TextResult — error messages can also wrap content
// from nodes (e.g., "node X not found: <description with bad bytes>").
func ErrorResult(msg string) ToolResult {
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: "Error: " + sanitizeUTF8(msg)}}, IsError: true}
}

// sanitizeUTF8 returns s when it is already valid UTF-8 (the hot path —
// strings.ToValidUTF8 short-circuits on valid input). Otherwise it returns
// a copy with every invalid byte sequence replaced by U+FFFD (the standard
// Unicode replacement character).
func sanitizeUTF8(s string) string {
	return strings.ToValidUTF8(s, "�")
}
