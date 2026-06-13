// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// GraphTypeToolDef returns the unified graph-type registration tool. Modeled on
// WorkerToolDef (worker_schema.go) — one tool with an `operation` enum rather
// than four top-level tools. A user registers ONE combined record per graph type
// that defines both how to collect it (an external binary) and how the system
// should treat its graph (summary/embed/sync behavior). The record is stored as
// a per-account, graph-resident config node and is the single source of truth
// read by both the client collector dispatch and the server behavior resolver.
//
// Schema-source-of-truth note: this definition is client-side.
// cmd/knowledge.loadSchemas appends GraphTypeToolDef() to the merged tool set
// that backs the tools/list response.
func GraphTypeToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "custom_collector",
		Description: "Register a custom external collector / plugin: a user-defined graph type backed by your own " +
			"collector binary. One combined record defines both how to collect the graph " +
			"(an external collector binary + its parameters) AND how the system should treat that graph " +
			"(summary/embed/sync behavior, with per-node-type overrides). " +
			"register: register a new custom collector / graph type. " +
			"update: edit an existing record (full re-write — supply every field you want persisted). " +
			"delete: remove a registered custom collector / graph type. " +
			"list: enumerate registered custom collectors with their collector binary + behavior. " +
			"Required params by operation (in addition to the always-required operation): " +
			"register requires name + collector.binary_path (absolute) + collector.param_transport (\"stdin\" or \"flag:<name>\"); " +
			"update requires name; delete requires name; list requires nothing further. " +
			"The registered name must NOT collide with a built-in graph type " +
			"(knowledge/code/cloud/cicd/practice/linkage/transformers/logs/web/pdf).",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "Operation to perform",
					Enum:        []string{"register", "update", "delete", "list"},
				},
				"name":        {Type: "string", Description: "Graph-type name (identity / lookup key — required for register/update/delete). Must not collide with a built-in graph type."},
				"description": {Type: "string", Description: "Human-facing one-liner describing the graph type (optional; used by register/update)."},
				"collector": {
					Type:                 "object",
					Description:          "Collector spec: how to populate the graph. Required for register.",
					AdditionalProperties: &falseValue,
					Properties: map[string]kgtools.Property{
						"binary_path":     {Type: "string", Description: "Absolute path to the collector binary."},
						"param_transport": {Type: "string", Description: "How params reach the binary: \"stdin\" or \"flag:<name>\"."},
						"param_schema":    {Type: "object", Description: "Map of param name -> {type: string|int|bool, required: bool}."},
					},
				},
				"behavior": {
					Type:                 "object",
					Description:          "Graph-level behavior defaults (the cascade defaults; per-node-type overrides go in node_types). Booleans are tri-state: omit to leave unset (inherit), set true/false to pin.",
					AdditionalProperties: &falseValue,
					Properties: map[string]kgtools.Property{
						"syncable":         {Type: "boolean", Description: "Whether graphs of this type may be synced."},
						"summarizable":     {Type: "boolean", Description: "Whether nodes of this type are summarized."},
						"embeddable":       {Type: "boolean", Description: "Whether nodes of this type are embedded."},
						"embed_fields":     {Type: "array", Items: &kgtools.Property{Type: "string"}, Description: "Node fields that participate in embedding."},
						"summarize_fields": {Type: "array", Items: &kgtools.Property{Type: "string"}, Description: "Node fields that participate in summarization."},
						"bm25_fields":      {Type: "array", Items: &kgtools.Property{Type: "string"}, Description: "Node fields that participate in BM25."},
						"extra":            {Type: "object", Description: "Forward-compat map of additional graph-behavior keys (string->string)."},
					},
				},
				"node_types": {Type: "object", Description: "Map of node-type -> behavior override {summarizable?, embeddable?, embed_fields?, summarize_fields?, bm25_fields?}. Any subset; unset means inherit the graph default."},
				"format":     {Type: "string", Description: "Output format: 'text' (default) or 'json'."},
			},
			Required: []string{"operation"},
		},
	}
}

// graphTypeArgs holds parsed arguments for the custom_collector tool. Field naming
// mirrors the schema property keys so json tags are 1:1. The nested objects
// (Collector, Behavior, NodeTypes) ride as json.RawMessage so they parse without
// bespoke arg structs — the handler unmarshals them into the gen
// *knowledgev1.GraphTypeDef shape via graphTypeDefFromArgs.
type graphTypeArgs struct {
	Operation   string          `json:"operation"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Collector   json.RawMessage `json:"collector"`
	Behavior    json.RawMessage `json:"behavior"`
	NodeTypes   json.RawMessage `json:"node_types"`
	Format      string          `json:"format"`
}
