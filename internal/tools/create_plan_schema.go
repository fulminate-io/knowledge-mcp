// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// CreatePlanToolDef returns the create_plan MCP tool definition.
// Relocated client-side per FUL-251 — the actual flow runs through
// InterceptCreatePlan (intercept_create_plan.go) against the projects
// package; the server has no create_plan handler. Wired into tools/list
// via the client's loadSchemas augmentation.
func CreatePlanToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "create_plan",
		Description: `Create an entire plan (plan + phases + steps + criteria + open questions) in a single call.
Phases are ordered sequentially by array position (each depends-on the previous).
Steps within a phase are also ordered sequentially by array position.
Open questions surface uncertainties for the user to answer before implementation.
Returns the full plan tree. Use this instead of individual create_project/create_phase/create_step calls.`,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":    {Type: "string", Description: "Plan name"},
				"goal":    {Type: "string", Description: "What this plan aims to achieve"},
				"summary": {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars (handler enforces). See docs/node-type-llm-defaults.md for why this is required on embed-only-knowledge types."},
				"phases": {Type: "array", Description: "Ordered list of phases. Each phase REQUIRES name and summary; each step REQUIRES name, description, and summary (handler enforces).", Items: &kgtools.Property{
					Type: "object", Description: `Phase object: {"name":"...","overview":"...","summary":"required search-optimized one-line summary, max 500 chars","steps":[{"name":"...","description":"...","summary":"required search-optimized one-line summary, max 500 chars","file_paths":"...","criteria":[{"description":"...","command":"...","type":"automated|manual"}]}]}`,
				}},
				"research_id": {Type: "string", Description: "Research project ID that informed this plan (optional — creates informed-by edge)"},
				"ticket_id":   {Type: "string", Description: "Ticket node ID to link this plan under (optional)"},
				"open_questions": {Type: "array", Description: "Open questions that need user input before implementation can proceed. Creates question nodes (status: open) linked to the plan.", Items: &kgtools.Property{
					Type: "object", Description: `Question object: {"question":"...","context":"why this question matters and what options exist"}`,
				}},
				"pattern_ids": {Type: "array", Description: "Canonical pattern node IDs this plan extends. Wired as plan→pattern uses edges. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. Broken/unknown IDs produce a non-fatal warning surfaced in the response (under a `## Warnings` section), not an error — v1 tolerates patterns that have not yet been encoded.", Items: &kgtools.Property{
					Type: "string", Description: "Pattern node ID",
				}},
				"no_patterns_reason": {Type: "string", Description: "Audited escape hatch when no pattern applies (trivial doc edit, scaffolding, etc.). Persisted as plan-node metadata `no_patterns_reason`. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied."},
				"proposed_patterns": {Type: "array", Description: "Not-yet-cataloged patterns this plan introduces. Each entry creates a pattern node with status='emerging' linked via uses. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied.", Items: &kgtools.Property{
					Type: "object", Description: `Proposed pattern: {"name":"...","sketch":"interface sketch / pseudocode describing the proposed pattern shape (optional)"}`,
				}},
				"language_patterns": {Type: "array", Description: "Language-specific defensive patterns/findings (e.g., Go anti-patterns from practice/go with metadata.dsl_pattern set) the plan should be vigilant of. Wired as plan→<finding|pattern> EdgeAudits edges. INDEPENDENT of pattern_ids / no_patterns_reason / proposed_patterns — accepts any non-empty subset, including none. Broken/unknown IDs produce a non-fatal warning under the `## Warnings` section.", Items: &kgtools.Property{Type: "string"}},
				"format":            {Type: "string", Description: "Output format: 'text' (default, walks the tree + warnings) or 'json' (structured: {id, name, node_ids, warnings})."},
			},
			Required: []string{"name", "goal", "summary", "phases"},
		},
	}
}
