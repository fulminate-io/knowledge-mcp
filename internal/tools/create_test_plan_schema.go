// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// CreateTestPlanToolDef returns the create_test_plan MCP tool definition.
// Relocated client-side — the actual flow runs through
// InterceptCreateTestPlan against the projects package; the server has
// no create_test_plan handler. Wired into tools/list via the client's
// loadSchemas augmentation.
func CreateTestPlanToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "create_test_plan",
		Description: `Create a reusable test plan (test_plan + test_steps + criteria) in a single call.
Test plans are templates that can be run multiple times — each run creates test_run nodes linked to the steps.
Steps are ordered sequentially by array position (each depends-on the previous).
Returns the full test plan tree.`,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":    {Type: "string", Description: "Test plan name"},
				"goal":    {Type: "string", Description: "What this test plan verifies"},
				"summary": {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars (handler enforces). See docs/node-type-llm-defaults.md for why this is required on embed-only-knowledge types."},
				"steps": {Type: "array", Description: "Ordered list of test steps. Each step REQUIRES name, description, and summary (handler enforces).", Items: &kgtools.Property{
					Type: "object", Description: `Step object: {"name":"...","description":"...","summary":"required search-optimized one-line summary, max 500 chars","criteria":[{"description":"...","command":"...","type":"automated|manual"}]}`,
					AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
						"name":        {Type: "string", Description: "Test step name (required)"},
						"description": {Type: "string", Description: "Test step description (required)"},
						"summary":     {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars"},
						"criteria":    {Type: "array", Description: "Pass/fail criteria for this test step.", Items: criterionItems()},
					},
				}},
				"format": {Type: "string", Description: "Output format: 'text' (default, walks the tree) or 'json' (structured: {id, name, step_ids})."},
			},
			Required: []string{"name", "goal", "summary", "steps"},
		},
	}
}
