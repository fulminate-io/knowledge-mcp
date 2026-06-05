// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// WhatNextToolDef returns the what_next MCP tool definition. Relocated
// client-side — the actual flow runs through InterceptWhatNext
// (intercept_what_next.go); the server has no what_next handler. Wired
// into tools/list via the client's loadSchemas augmentation.
func WhatNextToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "what_next",
		Description: "Find the next actionable steps: pending steps whose dependencies are all completed.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"project_id":         {Type: "string", Description: "Filter to a specific project (optional)"},
				"format":             {Type: "string", Description: "Output format: 'text' (default, concise), 'json' (structured), or 'ids' (newline-joined IDs only)."},
				"verbose":            {Type: "boolean", Description: "Include full Description bodies in text/JSON output. Default concise: title+id+type+status+parent-crumb only."},
				"include_containers": {Type: "boolean", Description: "Include actionable NodeProject + NodeTicket nodes in the result. Default false (steps + questions only)."},
			},
		},
	}
}
