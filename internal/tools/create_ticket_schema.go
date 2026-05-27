// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// CreateTicketToolDef returns the create_ticket MCP tool definition.
// Relocated client-side per FUL-251 — the actual flow runs through
// InterceptCreateTicket (intercept_create_ticket.go) against the projects
// package; the server has no create_ticket handler. Wired into tools/list
// via the client's loadSchemas augmentation.
func CreateTicketToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "create_ticket",
		Description: `Create a ticket node within a project. Tickets represent units of work. Returns the ticket ID.`,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":        {Type: "string", MaxLength: 255, Description: "Ticket name or title (synced to the Linear issue title, which caps at 255 chars)."},
				"project_id":  {Type: "string", Description: "Parent project node ID (required — links ticket to project)"},
				"description": {Type: "string", Description: "Ticket description"},
				"summary":     {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars (handler enforces). See docs/node-type-llm-defaults.md for why this is required on embed-only-knowledge types."},
				"external_id": {Type: "string", Description: "External tracker ID (e.g. JIRA-123, GH-456)"},
				"priority":    {Type: "string", Description: "Ticket priority (e.g. high, medium, low)"},
				"labels":      {Type: "string", Description: "Comma-separated labels or tags"},
				"pattern_ids": {Type: "array", Description: "Canonical pattern node IDs this ticket extends. Wired as ticket→pattern uses edges. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied. Broken/unknown IDs produce a non-fatal warning surfaced in the response (under a `## Warnings` section), not an error — v1 tolerates patterns that have not yet been encoded.", Items: &kgtools.Property{
					Type: "string", Description: "Pattern node ID",
				}},
				"no_patterns_reason": {Type: "string", Description: "Audited escape hatch when no pattern applies (trivial doc edit, scaffolding, etc.). Persisted as ticket-node metadata `no_patterns_reason`. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied."},
				"proposed_patterns": {Type: "array", Description: "Not-yet-cataloged patterns this ticket introduces. Each entry creates a pattern node with status='emerging' linked via uses. Exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be supplied.", Items: &kgtools.Property{
					Type: "object", Description: `Proposed pattern: {"name":"...","sketch":"interface sketch / pseudocode describing the proposed pattern shape (optional)"}`,
				}},
				"language_patterns": {Type: "array", Description: "Language-specific defensive patterns/findings (e.g., Go anti-patterns from practice/go with metadata.dsl_pattern set) the ticket should be vigilant of. Wired as ticket→<finding|pattern> EdgeAudits edges. INDEPENDENT of pattern_ids / no_patterns_reason / proposed_patterns — accepts any non-empty subset, including none. Broken/unknown IDs produce a non-fatal warning under the `## Warnings` section.", Items: &kgtools.Property{Type: "string"}},

				// Backend metadata passthrough. Pre-populated by the
				// client-side intercept after a successful Linear
				// write-through, then forwarded to the server in the
				// same mutate(create_batch) call.
				"backend":           {Type: "string", Description: "Backend identifier (e.g. \"linear\") to stamp on the ticket's `backend` metadata. Set by the client-side intercept after a successful remote create; never supplied by direct callers."},
				"linear_id":         {Type: "string", Description: "Linear-side ticket UUID returned by backend.CreateTicket. Maps to `linear_id` metadata."},
				"external_url":      {Type: "string", Description: "Deeplink URL to the remote ticket. Maps to `external_url` metadata."},
				"linear_project_id": {Type: "string", Description: "Linear-side project UUID for the parent project. Maps to `linear_project_id` metadata so the ticket carries an explicit backend-side parent pointer."},
				"linear_group_id":   {Type: "string", Description: "Linear team UUID inherited from the parent project. Maps to `linear_group_id` metadata."},
				"linear_group_key":  {Type: "string", Description: "Linear team key (e.g. \"ABC\") inherited from the parent project. Maps to `linear_group_key` metadata."},

				"format": {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured: {id, name, warnings})."},
			},
			Required: []string{"name", "project_id", "description", "summary"},
		},
	}
}
