// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// GraphTypeToolDef returns the READ-ONLY custom-collector tool. It stays
// op-dispatched with a single-value `operation` enum rather than becoming a bare
// tool, so a caller written against the retired write operations is refused by
// name and told what is admitted instead of having its arguments silently
// ignored.
//
// REGISTRATION IS A CONFIG FILE, NOT A TOOL CALL. A custom collector is an entry
// in `~/.knowledge/collectors.json` (user scope) or `<repo
// root>/.knowledge/collectors.json` (project scope), installed with `knowledge
// collector add`. This tool reports what the SERVER holds for those families —
// their behavior cascade — which is the half the routing gate and the
// summarize / embed / sync pipeline read.
//
// Schema-source-of-truth note: this definition is client-side.
// cmd/knowledge.loadSchemas appends GraphTypeToolDef() to the merged tool set
// that backs the tools/list response.
func GraphTypeToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "custom_collector",
		Description: "Read what the server holds for custom collectors: one operation, list, enumerating every " +
			"registered custom graph family with its summary/embed/sync behavior and its per-node-type overrides. " +
			"REGISTRATION IS A CONFIG FILE, NOT A TOOL CALL: a custom collector is an entry in " +
			"~/.knowledge/collectors.json (user scope) or <repo root>/.knowledge/collectors.json (project scope), " +
			"shaped like an MCP server entry (type stdio with command/args/env, or type http with url/headers) plus " +
			"a `tool` field naming the single MCP tool the daemon calls to collect, and installed with " +
			"`knowledge collector add | list | get | remove`. The entry name IS THE GRAPH FAMILY the collected " +
			"result lands in (the collect id is the instance), and must not collide with a built-in graph type " +
			"(knowledge/code/practice/linkage/checks/web/pdf), nor with a RETIRED one (cloud, logs, cicd), " +
			"whose refusal names the removal and the contrib route that replaced it. " +
			"THE TOOL'S SCHEMAS ARE A HARD REQUIREMENT: the tool an entry names must advertise both an input schema " +
			"and an output schema satisfying the collector contract (" + externalcollector.ContractSummary() + "), " +
			"checked when `knowledge collector add` writes the entry and again on every collect. " +
			"A family listed here with NO config entry is a legacy record: nothing resolves from it, and a collect " +
			"on that name fails naming the entry to write. Use `knowledge collector list` for the file side.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "Operation to perform. Registration is a config file: use `knowledge collector add|get|remove` to write one.",
					Enum:        []string{"list"},
				},
				"format": {Type: "string", Description: "Output format: 'text' (default) or 'json'."},
			},
			Required: []string{"operation"},
		},
	}
}

// graphTypeArgs holds parsed arguments for the custom_collector tool. Field
// naming mirrors the schema property keys so json tags are 1:1.
type graphTypeArgs struct {
	Operation string `json:"operation"`
	Format    string `json:"format"`
}
