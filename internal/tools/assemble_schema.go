// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// AssembleToolDef returns the assemble MCP tool definition. Relocated
// client-side — the rendering surface lives in
// cmd/knowledge/internal/projects/render and the wire intercept lives
// in intercept_assemble.go; the server has no assemble handler. Wired
// into tools/list via the client's loadSchemas augmentation.
func AssembleToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "assemble",
		Description: "Type-aware context assembly. Given a node (by id or type+name), assembles everything needed to work with it. For test plans, new_run=true creates a run session with pending test_run nodes.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"id":          {Type: "string", Description: "Node ID to assemble context for"},
				"name":        {Type: "string", Description: "Node name (used with type for name-based lookup)"},
				"type":        {Type: "string", Description: "Node type for name-based lookup (e.g. project, test_plan, research)"},
				"new_run":     {Type: "boolean", Description: "For test_plan: create a new run session with pending test_run nodes"},
				"run_session": {Type: "string", Description: "For test_plan: filter assembled test_runs by this run session UUID"},
				"format":      {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured)"},
				// DECLARED HERE OR REFUSED AT THE DOOR: intercept_assemble.go runs
				// rejectUndeclaredParams against this property set, so a param the
				// handler reads but the schema does not declare is rejected before
				// the handler ever sees it.
				"section_start": {Type: "integer", Description: "For a chunked plan: the first section index to return, zero-based and inclusive. Omit to start at the first section. An out-of-bounds, negative or inverted range errors naming the bound — it is never clamped."},
				"section_end":   {Type: "integer", Description: "For a chunked plan: the last section index to return, zero-based and inclusive. Omit to run to the last section. Supplying either bound returns the section BODIES in that range with their annotations; supplying neither returns the plan's index and tree alone, IN BOTH FORMATS — a text read shows each section's first 120 characters in the tree, and a json read omits the body and marks it body_omitted with its body_bytes, so neither default returns a whole plan. A range on a node that is not a plan is refused in both formats, and so is a range on a plan that HAS no sections — a phase-and-step plan has nothing to page."},
			},
		},
	}
}
