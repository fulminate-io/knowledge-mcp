// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// CreateProjectToolDef returns the create_project MCP tool definition.
// Relocated client-side per FUL-251 — the actual flow runs through
// InterceptCreateProject against the projects package; the server has
// no create_project handler. Wired into tools/list via the client's
// loadSchemas augmentation.
func CreateProjectToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name:        "create_project",
		Description: `Create a project node — a long-lived container for tickets. Returns the project ID.`,
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"name":        {Type: "string", MaxLength: 255, Description: "Project name (synced to the Linear project name, which caps at 255 chars)."},
				"description": {Type: "string", MaxLength: 249, Description: "Project description (must stay under 250 chars for Linear)."},
				"summary":     {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars. See docs/node-type-llm-defaults.md."},
				"group":       {Type: "string", Description: "Optional. Backend group key (Linear team key, Jira project key, GitHub repo, etc.) — required when LINEAR_API_KEY (or other backend env var) is set AND multiple groups exist; auto-defaults when only one group exists; ignored when no backend is enabled."},

				// Backend metadata passthrough. Pre-populated by the
				// client-side intercept after a successful Linear
				// write-through, then forwarded to the server in the
				// same mutate(create_batch) call.
				"backend":          {Type: "string", Description: "Backend identifier (e.g. \"linear\") to stamp on the project's `backend` metadata. Set by the client-side intercept after a successful remote create; never supplied by direct callers."},
				"linear_id":        {Type: "string", Description: "Linear-side project UUID returned by backend.CreateProject. Maps to `linear_id` metadata."},
				"external_url":     {Type: "string", Description: "Deeplink URL to the remote project. Maps to `external_url` metadata."},
				"linear_group_id":  {Type: "string", Description: "Linear team UUID the project landed under. Maps to `linear_group_id` metadata."},
				"linear_group_key": {Type: "string", Description: "Linear team key (e.g. \"ABC\") the project landed under. Maps to `linear_group_key` metadata."},

				"format": {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured: {id, name})."},
			},
			Required: []string{"name", "description", "summary"},
		},
	}
}
