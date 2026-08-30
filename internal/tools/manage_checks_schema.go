// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// ManageChecksToolDef returns the manage_checks MCP tool definition — the
// working surface for the deterministic corpus checks: authoring one with both
// fixtures in a single validated call, listing what the checks graph holds, and
// running checks against a repo's working tree.
//
// Client-owned, like every other tool in this file's family: the flow runs
// through InterceptManageChecks and the server has no manage_checks handler.
//
// THE OPERATION ENUM IS RENDERED FROM manageChecksOperations rather than typed
// again here, so the advertised vocabulary and the dispatch vocabulary cannot
// disagree — the refusal message renders from the same slice.
func ManageChecksToolDef() kgtools.MCPTool {
	fixture := func(role string) kgtools.Property {
		return kgtools.Property{
			Type: "object",
			Description: "The " + role + " fixture example node to author alongside the check. " +
				"Its content is the snippet the admission gate runs the check against.",
			AdditionalProperties: &falseValue,
			Properties: map[string]kgtools.Property{
				"name":        {Type: "string", Description: "Fixture node name."},
				"summary":     {Type: "string", Description: "Required search-optimized one-line summary, max 500 chars.", MaxLength: 500},
				"description": {Type: "string", Description: "Why this snippet is the " + role + " example."},
				"content":     {Type: "string", Description: "The fixture source text the check is run over."},
			},
		}
	}
	return kgtools.MCPTool{
		Name: "manage_checks",
		Description: "Author, inventory and run the deterministic corpus checks. " +
			"create authors a check and both fixtures in ONE validated call (nothing is written unless the check " +
			"fires on the bad example and is silent on the good one); list renders what the checks graph holds, " +
			"including fixtures no check binds; run executes checks over a repo's working tree and prefixes a " +
			"machine-readable verdict line. See help(\"manage_checks\").",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "What to do: " + strings.Join(manageChecksOperations, " | "),
					Enum:        append([]string(nil), manageChecksOperations...),
				},

				// Shared selectors.
				"language": {Type: "string", Description: "Tree-sitter language slug (e.g. 'go', 'python'). REQUIRED for run — it selects the checks corpus. Optional narrowing for list; omit it to list every language."},
				"repo":     {Type: "string", Description: "Code-graph name, or an absolute checkout path. REQUIRED for run — it names both the graph and the tree the checks walk."},

				// run only.
				"ids":         {Type: "array", Description: "run only: check node ids to execute. Omit to run every check in the corpus. An id matching no check is an error naming it, never a silent widening.", Items: &kgtools.Property{Type: "string"}},
				"path_prefix": {Type: "string", Description: "run only: repo-relative subtree the walk is narrowed to."},
				"top_k":       {Type: "integer", Description: "run only: cap on how many findings are rendered (0 = the analyzer's own ceilings apply)."},

				// create only.
				"name":         {Type: "string", Description: "create only: the check node's name."},
				"summary":      {Type: "string", Description: "create only: required search-optimized one-line summary of the check, max 500 chars.", MaxLength: 500},
				"description":  {Type: "string", Description: "create only: the check's prose guidance — what the rule is and why."},
				"content":      {Type: "string", Description: "create only: the check node's full content body."},
				"severity":     {Type: "string", Description: "create only: the severity its findings are emitted at (info | notice | warning | critical)."},
				"check_type":   {Type: "string", Description: "create only: the check's execution kind (ast_pattern | graph_assertion | topology_threshold | flow_model)."},
				"dsl_pattern":  {Type: "string", Description: "create only: the check body — for ast_pattern, an ast DSL pattern."},
				"check_where":  {Type: "string", Description: "create only: an optional ast where-tree as JSON text."},
				"fixture_bad":  fixture("bad"),
				"fixture_good": fixture("good"),

				"format": {Type: "string", Description: "Output format: 'text' (default) or 'json'."},
			},
			Required: []string{"operation"},
		},
	}
}
