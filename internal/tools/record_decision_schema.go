// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// RecordDecisionToolDef returns the record_decision MCP tool definition.
// Relocated client-side — the actual flow runs through
// InterceptRecordDecision (intercept_record_decision.go) against the
// projects package; the server has no record_decision handler. Wired
// into tools/list via the client's loadSchemas augmentation.
func RecordDecisionToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "record_decision",
		Description: "Record a design decision with full rationale. Links to evidence that informed it. This is the most valuable knowledge for cross-session continuity.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":         {Type: "string", Description: "Decision name (e.g., 'Keep HNSW in blob, drop only BM25')"},
				"choice":       {Type: "string", Description: "What was decided"},
				"rationale":    {Type: "string", Description: "Why this was chosen"},
				"alternatives": {Type: "string", Description: "Other options considered (comma-separated)"},
				"informed_by":  {Type: "string", Description: "Comma-separated node IDs of findings/research that informed this decision"},
				"format":       {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured: {id, name, warnings})."},
			},
			Required: []string{"name", "choice", "rationale"},
		},
	}
}
