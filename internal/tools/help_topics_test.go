// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHelpTopics_NewTopicsDispatch verifies the reference topics
// (topology, recipes) dispatch to non-empty content and surface their
// key markers. A regression here means CLAUDE.md's pointers to help()
// would 404.
func TestHelpTopics_NewTopicsDispatch(t *testing.T) {
	cases := []struct {
		topic   string
		markers []string
	}{
		{
			topic:   "topology",
			markers: []string{"Topology Analyzers", "query(mode=\"topology\")", "pagerank", "betweenness", "Adding a new analyzer"},
		},
		{
			topic: "ast",
			markers: []string{
				"Structural code search",
				"tree-sitter",
				"Placeholder DSL", "$X", "$$$",
				"match", "count", "explain", "list_node_kinds",
				"where-tree",
				"inside_pattern",
				"defer $X.Close()",
				"language",
				"include_tests",
			},
		},
		{
			topic: "thoughts",
			markers: []string{
				"Persistent reasoning graph",
				`operation: "think"`,
				`operation: "charge"`,
				`operation: "recall"`,
				`operation: "trace"`,
				`operation: "propagate"`,
				"DeGroot",
				"valence",
				"magnitude",
			},
		},
		{
			topic: "recipes",
			markers: []string{
				"Recipe DSL",
				"select", "traverse", "filter", "bind", "group_by",
				"emit", "lookup", "link", "source_ref",
				"~=", "!~",
				"as $var",
				"StableID", "translated-from",
				"lookups_resolved", "lookup_misses", "link_misses",
				"Cross-emit bindings",
				"cloneRowVars",
				"dry_run",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.topic, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"topic": tc.topic})
			if err != nil {
				t.Fatal(err)
			}
			res := handleHelpClient(args)
			if res.IsError {
				t.Fatalf("help(%s) returned error: %q", tc.topic, resultText(res))
			}
			text := resultText(res)
			if text == "" {
				t.Fatalf("help(%s) returned empty content", tc.topic)
			}
			for _, m := range tc.markers {
				if !strings.Contains(text, m) {
					t.Errorf("help(%s) missing marker %q", tc.topic, m)
				}
			}
		})
	}
}

// TestHelpTopics_OverviewLinksNewTopics verifies the overview text references
// the new topics so callers can discover them.
func TestHelpTopics_OverviewLinksNewTopics(t *testing.T) {
	args, err := json.Marshal(map[string]string{"topic": "overview"})
	if err != nil {
		t.Fatal(err)
	}
	res := handleHelpClient(args)
	if res.IsError {
		t.Fatalf("help(overview) returned error: %q", resultText(res))
	}
	text := resultText(res)
	for _, marker := range []string{`help("topology")`, `help("recipes")`, `help("ast")`} {
		if !strings.Contains(text, marker) {
			t.Errorf("overview missing marker %q", marker)
		}
	}
}

// TestHelpOverview_DocumentsReadConsistencyContract pins the measured
// read-consistency contract in the overview topic. This is the surface
// agents actually read, and its absence is what let "I must have read
// stale data" stand as a first hypothesis whenever two sessions
// disagreed about a node's text — an explanation the measurements rule
// out. Each marker carries one load-bearing clause of the contract:
// the section itself, the guarantee, the no-window claim, the
// discipline that replaces the stale-read hypothesis, and the field a
// reader cites to date someone else's read.
func TestHelpOverview_DocumentsReadConsistencyContract(t *testing.T) {
	args, err := json.Marshal(map[string]string{"topic": "overview"})
	if err != nil {
		t.Fatal(err)
	}
	res := handleHelpClient(args)
	if res.IsError {
		t.Fatalf("help(overview) returned error: %q", resultText(res))
	}
	text := resultText(res)
	for _, marker := range []string{
		"Read consistency",
		"read-your-writes",
		"no stale-read window",
		"Re-fetch immediately before filing",
		"updated_at",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("overview missing read-consistency marker %q", marker)
		}
	}
}
