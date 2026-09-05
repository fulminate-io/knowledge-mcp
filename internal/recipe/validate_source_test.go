// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// midRecaseBody is a FROZEN COPY of the canned section-render body shipped in
// help("recipes"), with ONE change: its retired string predicate migrated to the
// where-tree form. The lowercase `contains` arguments are exactly as shipped.
//
// IT IS FROZEN ON PURPOSE AND IT DIVERGES FROM THE SHIPPED FILE. Two reasons,
// both measured. First, `go list -deps` shows internal/tools depends on
// internal/recipe and NOT the reverse, so a test in THIS package is structurally
// incapable of reading the tools package's help const — the fixture cannot be a
// live read. Second, the shipped body is re-cased to CONTAINS by this same
// changeset, so a fixture kept in sync with it would carry no defect and this
// subtest would silently assert nothing. Anyone who "repairs" this literal to
// match the shipped file vacates the gate entirely.
//
// It is the MID-RECASE shape rather than the shipped pre-edit bytes because this
// changeset makes the legacy predicate a PARSE error: a verbatim freeze would be
// refused before the validator this subtest exercises was ever reached.
const midRecaseBody = `select section
filter {"matches": {"of": "section.symbol_name", "regex": "^[A-Z]"}}
emit pattern {
    name := section.symbol_name
    path := heading_path("contains", "symbol_name", " > ")
    body := subtree_concat("contains", "body", "\n\n", "4")
}`

// validatorFixture is a CONTAINS-only graph, mirroring the shape a collected pdf
// source graph actually has: one edge type, in upper case, and metadata keys
// stamped by one node type each.
func validatorFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "d1", Type: "document", SymbolName: "Doc"},
			{Id: "s1", Type: "section", SymbolName: "Chapter One", Metadata: map[string]string{"level": "1"}},
			{Id: "p1", Type: "paragraph", SymbolName: "para", Content: "text", Metadata: map[string]string{"page_first": "3"}},
		},
		edges: []*knowledgev1.Edge{
			svEdge("d1", "s1", "CONTAINS"),
			svEdge("s1", "p1", "CONTAINS"),
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
	require.NoError(t, err)
	return sv
}

// refusalFor parses a body and interprets it against the fixture, returning the
// refusal text. It drives the REAL entry point, so a validator wired somewhere
// Interpret never reaches cannot pass.
func refusalFor(t *testing.T, sv *sourceView, body string) string {
	t.Helper()
	r, err := Parse([]byte(body))
	require.NoError(t, err, "the body must PARSE — this subtest is about validation, not grammar")
	_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
	require.Error(t, err, "the recipe must be refused")
	return err.Error()
}

func TestValidateAgainstSource_Refusals(t *testing.T) {
	sv := validatorFixture(t)

	t.Run("unknown_select_type", func(t *testing.T) {
		msg := refusalFor(t, sv, "select sectionn\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, `"sectionn"`, "the offending value")
		assert.Contains(t, msg, `did you mean "section"?`, "the near-miss, from the edit-distance pass")
		assert.Contains(t, msg, "pdf/doc", "the graph it was checked against")
		assert.Contains(t, msg, "(3 nodes)", "with its size")
		assert.Contains(t, msg, `"document", "paragraph", "section"`, "the observed vocabulary, sorted")
		assert.Contains(t, msg, "refused before the walk", "and why it was not answered with zero rows")
	})

	t.Run("unknown_node_type_in_kind_leaf", func(t *testing.T) {
		msg := refusalFor(t, sv,
			"select section where {\"kind\": {\"of\": \"node\", \"is\": \"sectionn\"}}\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, "kind leaf")
		assert.Contains(t, msg, `"sectionn"`)
		assert.Contains(t, msg, `did you mean "section"?`)
	})

	t.Run("unknown_metadata_key_in_where_tree", func(t *testing.T) {
		msg := refusalFor(t, sv,
			"select section\nfilter {\"exists\": {\"of\": \"section.metadata.no_such_key\"}}\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, "where-tree")
		assert.Contains(t, msg, `"no_such_key"`)
		assert.Contains(t, msg, `"level", "page_first"`, "the graph-wide key union is listed")
	})

	t.Run("unknown_metadata_key_in_emit_expression", func(t *testing.T) {
		// The SECOND carrier: a validator covering only where-tree `of` values
		// passes the subtest above and fails this one.
		msg := refusalFor(t, sv,
			"select section\nemit pattern {\n    name := node.symbol_name\n    extra := section.metadata.no_such_key\n}")
		assert.Contains(t, msg, `"no_such_key"`)
		assert.Contains(t, msg, "metadata key")
	})

	t.Run("miscased_edge_type", func(t *testing.T) {
		// CARRIER 1 OF 3: the traverse rule. BOTH halves are asserted — the run is
		// REFUSED because the comparison is exact-case, and the message NAMES the
		// casing sibling, which only pass 1 of the suggest helper can produce.
		// Asserting the refusal beside the suggestion is what stops the cheapest
		// wrong route: an implementer who satisfies the suggestion by folding the
		// comparison makes the refusal fail.
		msg := refusalFor(t, sv,
			"select section\ntraverse contains out as $t\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, `"contains"`)
		assert.Contains(t, msg, `did you mean "CONTAINS"?`)
		assert.Contains(t, msg, "casing")
		assert.Contains(t, msg, "matched exactly")
	})

	t.Run("miscased_edge_type_in_ancestor_leaf", func(t *testing.T) {
		// CARRIER 2 OF 3: a where-tree walk leaf.
		msg := refusalFor(t, sv,
			"select section\nfilter {\"ancestor\": {\"edge\": \"contains\", \"where\": {\"kind\": {\"of\": \"node\", \"is\": \"document\"}}}}\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, "ancestor leaf")
		assert.Contains(t, msg, `"contains"`)
		assert.Contains(t, msg, `did you mean "CONTAINS"?`)
	})

	t.Run("miscased_edge_type_in_builtin_argument", func(t *testing.T) {
		// CARRIER 3 OF 3, the one the PROJECTION side runs on. A validator
		// censusing only the traverse rule passes both subtests above and fails
		// this one.
		msg := refusalFor(t, sv, midRecaseBody)

		// LEG 1 — EVERY SITE IS NAMED. The body passes a lowercase edge type to TWO
		// builtins inside ONE emit block, and RuleEmit.Fields is a Go map with
		// randomized iteration order, so a first-error-wins census can NEVER name
		// both: measured at roughly 425 to 75 over 500 runs of identical input.
		assert.Contains(t, msg, "heading_path", "the first mis-cased argument is named")
		assert.Contains(t, msg, "subtree_concat", "and so is the second")
		assert.Contains(t, msg, `did you mean "CONTAINS"?`)

		// LEG 2 — THE MESSAGE IS BYTE-IDENTICAL ACROSS REPEATED RUNS. This rejects
		// a DIFFERENT wrong implementation from the one leg 1 rejects: a validator
		// that collects every violation but reports them in map order names both
		// builtins and still varies run to run. Leg 1 cannot see that; leg 2 can.
		// The repetition lives INSIDE the subtest, so the test cache stays
		// authoritative and no force-rerun flag is needed.
		r, err := Parse([]byte(midRecaseBody))
		require.NoError(t, err)
		first := ""
		for i := range 200 {
			_, _, err := validateAgainstSource(r, sv)
			require.Error(t, err)
			if i == 0 {
				first = err.Error()
				continue
			}
			require.Equal(t, first, err.Error(), "refusal not deterministic at run %d", i)
		}
		assert.Contains(t, first, "heading_path")
		assert.Contains(t, first, "subtree_concat")
		assert.Less(t, strings.Index(first, "heading_path"), strings.Index(first, "subtree_concat"),
			"and the sites are reported in a fixed order")
	})

	t.Run("every_edge_taking_builtin_is_censused", func(t *testing.T) {
		// The six builtins that take an edge type as their first argument. A table
		// covering four of them returns identical answers for the other two.
		cases := map[string]string{
			"has_edge":         `has_edge("contains", "out")`,
			"children_concat":  `children_concat("contains", "body", "\n")`,
			"ancestors_concat": `ancestors_concat("contains", "symbol_name", " > ")`,
			"has_ancestor":     `has_ancestor("contains", "symbol_name", "^Part")`,
			"heading_path":     `heading_path("contains", "symbol_name", " > ")`,
			"subtree_concat":   `subtree_concat("contains", "body", "\n\n", "4")`,
		}
		for name, call := range cases {
			t.Run(name, func(t *testing.T) {
				msg := refusalFor(t, sv,
					"select section\nemit pattern {\n    name := node.symbol_name\n    v := "+call+"\n}")
				assert.Contains(t, msg, name)
				assert.Contains(t, msg, `"contains"`)
				assert.Contains(t, msg, `did you mean "CONTAINS"?`)
			})
		}
	})

	t.Run("correctly_cased_builtin_argument_is_admitted", func(t *testing.T) {
		// THE CONTROL. Without it, a validator that refused every edge-type
		// argument would pass every subtest above.
		r, err := Parse([]byte("select section\nemit pattern {\n    name := node.symbol_name\n    path := heading_path(\"CONTAINS\", \"symbol_name\", \" > \")\n}"))
		require.NoError(t, err)
		_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err, "the exact casing the graph carries is admitted")
	})

	t.Run("non_literal_edge_type_argument", func(t *testing.T) {
		// A DIFFERENT property from the census: this gates the REFUSAL of a
		// non-literal. A validator that refuses non-literals and never censuses
		// the literals it accepts passes this and fails every carrier subtest.
		msg := refusalFor(t, sv,
			"select section\nbind $e := \"CONTAINS\"\nemit pattern {\n    name := node.symbol_name\n    path := heading_path($e, \"symbol_name\", \" > \")\n}")
		assert.Contains(t, msg, "heading_path")
		assert.Contains(t, msg, "must be a string literal")
		assert.Contains(t, msg, "before the walk")
	})

	t.Run("compare_unknown_operator", func(t *testing.T) {
		// The compare leaf is REAL now, so the refusal that replaces the reserved
		// one is the unknown-operator refusal — and it carries the three things an
		// author repairs from when the recipe body lives in the graph rather than
		// in a file they can open: the offending spelling quoted, the admitted
		// operator list, and that the run was refused BEFORE the walk rather than
		// answered with zero rows.
		msg := refusalFor(t, sv,
			"select section\nfilter {\"compare\": {\"of\": \"node.metadata.level\", \"op\": \"gtt\", \"value\": \"0\"}}\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, `"gtt"`)
		assert.Contains(t, msg, "eq, gt, gte, lt, lte, ne")
		assert.Contains(t, msg, "before the walk")
	})

	t.Run("unknown_builtin", func(t *testing.T) {
		msg := refusalFor(t, sv,
			"select section\nemit pattern {\n    name := lowerr(node.symbol_name)\n}")
		assert.Contains(t, msg, `unknown builtin "lowerr"`)
		assert.Contains(t, msg, `did you mean "lower"?`)
		assert.Contains(t, msg, "subtree_concat", "the builtin vocabulary is listed")
	})

	t.Run("wrong_builtin_arity", func(t *testing.T) {
		msg := refusalFor(t, sv,
			"select section\nemit pattern {\n    name := trim(node.symbol_name, \"extra\")\n}")
		assert.Contains(t, msg, "trim")
		assert.Contains(t, msg, "expected 1 argument(s), got 2")
	})

	t.Run("uncompilable_regex_literal", func(t *testing.T) {
		msg := refusalFor(t, sv,
			"select section\nfilter {\"matches\": {\"of\": \"section.symbol_name\", \"regex\": \"^(unclosed\"}}\nemit pattern {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, "does not compile")
		assert.Contains(t, msg, "^(unclosed")
	})

	t.Run("every_violation_is_collected", func(t *testing.T) {
		// The collect-and-sort contract at the level it was written for: three
		// unrelated mistakes in one recipe are all named in one refusal, so the
		// author repairs every site in one pass rather than one per re-run.
		msg := refusalFor(t, sv, `select sectionn
traverse contains out as $t
emit pattern {
    name := node.symbol_name
    extra := node.metadata.no_such_key
}`)
		assert.Contains(t, msg, `"sectionn"`)
		assert.Contains(t, msg, `"contains"`)
		assert.Contains(t, msg, `"no_such_key"`)
	})
}

func TestValidateAgainstSource_AcceptsCorrectRecipes(t *testing.T) {
	sv := validatorFixture(t)

	t.Run("metadata_census_is_graph_wide_union", func(t *testing.T) {
		// page_first is stamped by PARAGRAPH nodes only, and this recipe selects
		// sections. A per-node-type census refuses this correct recipe.
		r, err := Parse([]byte("select section\ntraverse CONTAINS out as $t\nemit pattern {\n    name := node.symbol_name\n    page := node.metadata.page_first\n}"))
		require.NoError(t, err)
		_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err, "a key only one node type stamps is still part of the graph's vocabulary")
	})

	t.Run("target_types_and_link_rel_are_not_censused", func(t *testing.T) {
		// The emit type, the lookup type and the link relationship all name the
		// TARGET graph and appear in neither source vocabulary. A validator applied
		// to the target side refuses this correct recipe.
		r, err := Parse([]byte(`select section
emit pattern {
    name := node.symbol_name
} as $a
lookup pattern by node.symbol_name as $b
link $a --[applies-when]--> $b`))
		require.NoError(t, err)
		_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err, "target-graph vocabulary is not the source census's business")
	})

	t.Run("well_known_node_fields_are_never_censused", func(t *testing.T) {
		// The node's own struct fields are legal on every graph whatever the
		// corpus stamped; censusing them against metadata keys would refuse
		// essentially every recipe.
		r, err := Parse([]byte("select section\nemit pattern {\n    name := node.symbol_name\n    summary := node.description\n    text := node.body\n    ident := node.id\n}"))
		require.NoError(t, err)
		_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err)
	})
}

// TestInterpret_SourceCensus_ComputedOncePerRun is a PERFORMANCE observable, and
// it detects a class every logic gate is blind to: a validator that walks the
// index maps inline at each of its sites returns identical correct answers on
// every input while re-walking the whole graph once per site.
//
// Comparing the POINTER census() returns would prove only what sync.Once
// guarantees structurally — and would be satisfied by a validator that never
// calls census() at all. The WALK COUNT is what makes the property countable.
func TestInterpret_SourceCensus_ComputedOncePerRun(t *testing.T) {
	sv := validatorFixture(t)
	require.Zero(t, sv.censusWalks, "nothing has asked for the vocabulary yet")

	// A recipe carrying SEVERAL censused sites: a select type, a traverse edge
	// type, two where-tree leaves, an emit field path and a builtin edge
	// argument. A per-site walk would count six, not one.
	r, err := Parse([]byte(`select section
traverse CONTAINS out as $t
filter {"all": [
    {"kind": {"of": "node", "is": "paragraph"}},
    {"ancestor": {"edge": "CONTAINS", "where": {"kind": {"of": "node", "is": "section"}}}}
]}
emit pattern {
    name := node.symbol_name
    page := node.metadata.page_first
    path := heading_path("CONTAINS", "symbol_name", " > ")
}`))
	require.NoError(t, err)

	_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err, "the recipe is correct; this test measures cost, not refusal")
	assert.Equal(t, 1, sv.censusWalks, "the graph is walked exactly once for the whole run")
}

// TestValidateAgainstSource_IssuesNoExtraExecuteRPC pins the property
// loadSourceView's whole design exists to hold: validation reads the resident
// indexes and issues NO wire read of its own.
//
// The baseline is the load itself — two Execute calls, one page of nodes and one
// of edges. An implementer who sees this number move should treat it as a real
// finding rather than expected drift: nothing on this path performs writes or
// opens a transaction.
func TestValidateAgainstSource_IssuesNoExtraExecuteRPC(t *testing.T) {
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "s1", Type: "section", SymbolName: "Chapter One", Metadata: map[string]string{"level": "1"}},
			{Id: "p1", Type: "paragraph", SymbolName: "para"},
		},
		edges: []*knowledgev1.Edge{svEdge("s1", "p1", "CONTAINS")},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
	require.NoError(t, err)
	loadOnlyBaseline := f.calls
	require.Equal(t, 2, loadOnlyBaseline, "the load itself is two Execute calls")

	r, err := Parse([]byte(`select section
traverse CONTAINS out as $t
emit pattern {
    name := node.symbol_name
    lvl := node.metadata.level
}`))
	require.NoError(t, err)
	_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	assert.Equal(t, loadOnlyBaseline, f.calls,
		"a fully validated run issues no Execute beyond the load")
}
