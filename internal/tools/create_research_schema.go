// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// CreateResearchToolDef returns the create_research MCP tool definition.
// Relocated client-side — the actual flow runs through
// InterceptCreateResearch against the projects package; the server has
// no create_research handler. Wired into tools/list via the client's
// loadSchemas augmentation.
func CreateResearchToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "create_research",
		Description: `Create a structured research project with ordered sub-questions in a single call.
Questions are ordered sequentially by array position (each depends-on the previous).
Findings are added later via record_finding with question_id. Use this for complex investigations with multiple sub-questions.`,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":      {Type: "string", Description: "Research project name"},
				"goal":      {Type: "string", Description: "What this research aims to answer"},
				"summary":   {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars (handler enforces)."},
				"ticket_id": {Type: "string", Description: "Ticket node ID to link this research under (optional)"},
				"questions": {Type: "array", Description: "Ordered list of research questions. Each question REQUIRES a summary (search-optimized rendering of the question itself, distinct from context which is background).", Items: &kgtools.Property{
					Type: "object", Description: `Question object: {"question":"...","summary":"required search-optimized one-line summary, max 500 chars","context":"why this question matters"}`,
					AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
						"question": {Type: "string", Description: "The research question (required)"},
						"summary":  {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars"},
						"context":  {Type: "string", Description: "Why this question matters (background)"},
					},
				}},
				"format": {Type: "string", Description: "Output format: 'text' (default, walks the tree) or 'json' (structured: {id, name, question_ids})."},
			},
			Required: []string{"name", "goal", "summary", "questions"},
		},
	}
}
