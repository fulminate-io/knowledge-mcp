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
				"ids":           {Type: "array", Description: "run only: check node ids to execute. Omit to run every check in the corpus. An id matching no check is an error naming it, never a silent widening.", Items: &kgtools.Property{Type: "string"}},
				"path_prefix":   {Type: "string", Description: "run only: repo-relative subtree the walk is narrowed to. Prefixes match whole path SEGMENTS, so 'pkg' is the pkg directory and never pkgextra. A prefix that reached NO FILE of the corpus language is REFUSED naming the prefix — a mistyped or over-specific scope is never reported as a clean corpus, because a scan that opened no file is not a clean scan."},
				"include_tests": {Type: "boolean", Description: "run only: walk this language's TEST files too. Omitted (the default) walks non-test files only, and is legal for every language; an explicit true or false for a language ast carries no test-file convention for is REFUSED naming the language, because there the flag would decide nothing. A check may instead declare applies_to_tests on its own node, which widens that check alone and needs no knob here."},
				"top_k":         {Type: "integer", Description: "run only: cap on how many findings are rendered (0 = no cap, the analyzer's own ceilings apply). It bounds ONLY the rendered body and never reaches the classification: the verdict line and its counts are folded over every finding the run produced. A render that was clipped states the total, as 'returning X of Y findings'. The admitted range is 0 or a positive count; a NEGATIVE value is REFUSED naming it, rather than coerced into a second spelling of no cap."},

				// create only.
				"name":             {Type: "string", Description: "create only: the check node's name."},
				"summary":          {Type: "string", Description: "create only: required search-optimized one-line summary of the check, max 500 chars.", MaxLength: 500},
				"description":      {Type: "string", Description: "create only: the check's prose guidance — what the rule is and why."},
				"content":          {Type: "string", Description: "create only: the check node's full content body."},
				"severity":         {Type: "string", Description: "create only: the severity its findings are emitted at (info | notice | warning | critical)."},
				"check_type":       {Type: "string", Description: "create only: the check's execution kind (ast_pattern | graph_assertion | topology_threshold | flow_model)."},
				"dsl_pattern":      {Type: "string", Description: "create only: the check body — for ast_pattern, an ast DSL pattern."},
				"check_where":      {Type: "string", Description: "create only: an optional ast where-tree as JSON text."},
				"applies_to_tests": {Type: "boolean", Description: "create only: declare that this check's defect class lives in TEST files, so a run widens the walk for this check alone with no run-wide include_tests. Omitted or false writes no declaration; true is refused for a language ast carries no test-file convention for, where it would widen nothing."},
				"fixture_bad":      fixture("bad"),
				"fixture_good":     fixture("good"),

				"format": {Type: "string", Description: "Output format: 'text' (default) or 'json'."},
			},
			Required: []string{"operation"},
		},
	}
}
