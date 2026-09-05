// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// walk_validate_source_test.go drives the walk rule through Interpret rather
// than through evalWalk, and that is the whole reason the file exists.
// validateAgainstSource runs INSIDE Interpret: a test calling evalWalk directly
// never reaches it, so both halves of the source-census collision — the missing
// RuleWalk arm on checkRule and the hardcoded `group` skip-list — shipped green
// past every gate that stops at the evaluator.

// admittedBy parses a body and interprets it against the fixture, requiring the
// run to be ADMITTED. It is refusalFor's opposite number, and both drive the
// same real entry point.
func admittedBy(t *testing.T, sv *sourceView, body string) {
	t.Helper()
	r, err := Parse([]byte(body))
	require.NoError(t, err, "the body must PARSE — this subtest is about validation, not grammar")
	_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err, "the recipe must be admitted")
}

// TestWalk_AgainstSourceValidator is a six-row matrix with a FIRING control and
// a SILENT control, so a green matrix cannot mean the validator never ran.
//
// The fixture carries metadata keys "level" and "page_first" only, so neither
// "depth" nor "position" is admitted by accident.
func TestWalk_AgainstSourceValidator(t *testing.T) {
	sv := validatorFixture(t)

	const emitTail = `
emit pattern {
    name := node.symbol_name
}`

	// FIRING CONTROL: the census is live on this fixture, through the rule that
	// already had an arm. Without this row a matrix of walk refusals could be
	// green because nothing was censused at all.
	t.Run("traverse_unknown_edge_is_refused_firing_control", func(t *testing.T) {
		msg := refusalFor(t, sv, "select section\ntraverse NOSUCHEDGE out"+emitTail)
		assert.Contains(t, msg, `"NOSUCHEDGE"`)
	})

	// The RuleWalk census arm. Without it RuleWalk falls through checkRule's
	// switch, which has no default, and the walk's edge type escapes the census.
	t.Run("walk_unknown_edge_is_refused", func(t *testing.T) {
		msg := refusalFor(t, sv, "select section\nwalk NOSUCHEDGE"+emitTail)
		assert.Contains(t, msg, `"NOSUCHEDGE"`)
		assert.Contains(t, msg, "walk:", "the refusal names the walk as its site")
	})

	// Edge types are matched EXACTLY, including case. With the census arm in
	// place a mis-cased spelling is refused before the walk instead of answered
	// with zero rows.
	t.Run("walk_miscased_edge_is_refused", func(t *testing.T) {
		msg := refusalFor(t, sv, "select section\nwalk contains"+emitTail)
		assert.Contains(t, msg, `"contains"`)
		assert.Contains(t, msg, "matched exactly")
	})

	// The declared pseudo-variable namespace. Without `walk` in it the census
	// reads walk.depth as a node field and refuses the feature's own headline
	// recipe before it runs.
	t.Run("walk_pseudo_variables_are_admitted", func(t *testing.T) {
		admittedBy(t, sv, `select document
walk CONTAINS
emit pattern {
    name := node.symbol_name
    d := walk.depth
    p := walk.position
}`)
	})

	// SILENT CONTROL: the namespace that was already there still works, so
	// replacing a hardcoded comparison with a declared set did not drop it.
	t.Run("group_keys_is_still_admitted_silent_control", func(t *testing.T) {
		admittedBy(t, sv, `select section
group_by section.symbol_name
emit pattern {
    name := group.keys
}`)
	})

	// THE INVERSE DIRECTION ON THE WALK'S OWN NAMESPACE, which the node-head row
	// below cannot see: it drives a NODE head, so it stays green while every name
	// under `walk.` is admitted. A typo here is the silent-empty class the whole
	// validator exists to close — walk.position is legitimately empty when no
	// position is determinable, so an empty value is not a tell an author can use.
	t.Run("walk_unknown_pseudo_variable_is_refused", func(t *testing.T) {
		msg := refusalFor(t, sv, `select document
walk CONTAINS
emit pattern {
    name := node.symbol_name
    d := walk.levl
}`)
		assert.Contains(t, msg, "walk.levl", "the refusal names the offending read")
		assert.Contains(t, msg, "walk.depth", "and lists the names that are stamped")
	})

	// The passing control for the row above, in the same run: the two names the
	// walk rule actually stamps stay admitted, so the narrowing did not simply
	// refuse the namespace wholesale.
	t.Run("walk_declared_pseudo_variables_are_still_admitted", func(t *testing.T) {
		admittedBy(t, sv, `select document
walk CONTAINS
emit pattern {
    name := node.symbol_name
    d := walk.depth
    p := walk.position
}`)
	})

	// A BARE PSEUDO-VARIABLE HEAD NAMES NO VALUE. `walk` alone is not something
	// any rule stamps, and before the census was narrowed it fell past the
	// length guard to the node read and answered the ROW'S SYMBOL NAME for a
	// question about a pseudo-variable. THE SAME IS NOW TRUE OF BARE `group`,
	// which 9b8a0609 admitted silently — a behaviour change this row is what
	// records. Both are refused by the same path, which is why one subtest
	// covers the pair.
	t.Run("a_bare_pseudo_variable_head_is_refused", func(t *testing.T) {
		walkMsg := refusalFor(t, sv, `select document
walk CONTAINS
emit pattern {
    name := node.symbol_name
    d := walk
}`)
		assert.Contains(t, walkMsg, "walk", "the refusal names the offending read")
		assert.Contains(t, walkMsg, "walk.depth", "and lists the names that are stamped")

		groupMsg := refusalFor(t, sv, `select section
group_by section.symbol_name
emit pattern {
    name := group
}`)
		assert.Contains(t, groupMsg, "group.keys",
			"bare `group` is refused on the same path, which it was not before the narrowing")
	})

	// THE INVERSE DIRECTION. Widening the skip set to admit everything is the
	// easy way to make the row above green, and this row is what rejects it.
	t.Run("an_unknown_metadata_key_is_still_refused", func(t *testing.T) {
		msg := refusalFor(t, sv, `select section
emit pattern {
    name := node.nosuchkey
}`)
		assert.Contains(t, msg, `"nosuchkey"`)
	})
}

// TestWalk_AliasBindsOnEveryWalkedRow gates the `as $var` half of the grammar,
// which every other walk assertion leaves untouched: deleting the alias
// assignment from evalWalk changes no row, no depth and no position, so without
// this test the binding the step prescribes is unenforced.
//
// The recipe reads the alias AS THE EMIT'S IDENTITY, so an unbound alias is not
// a wrong value but NO ROWS AT ALL — an emit with an empty name and no identity
// is skipped, which is the red.
func TestWalk_AliasBindsOnEveryWalkedRow(t *testing.T) {
	sv := validatorFixture(t)

	t.Run("alias_is_readable_from_every_row", func(t *testing.T) {
		r, err := Parse([]byte(`select document
walk CONTAINS as $n
emit pattern {
    name := $n
}`))
		require.NoError(t, err)
		res, err := Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip",
			Options{Extract: true, SourceManifest: FormatSourceManifest("doc", "inline")})
		require.NoError(t, err)
		require.NotNil(t, res.Extract)

		require.Len(t, res.Extract.Rows, 2,
			"an unbound alias makes every row identity-less and yields rows=0")
		for _, row := range res.Extract.Rows {
			assert.Equal(t, row.SourceNodeID, row.Fields["name"],
				"the alias resolves to the walked row's own node id")
		}
	})

	// The control: the binding happens only when the recipe asks for it, so the
	// subtest above is measuring the `as` clause rather than some ambient value.
	t.Run("no_as_clause_leaves_the_var_unbound", func(t *testing.T) {
		r, err := Parse([]byte(`select document
walk CONTAINS
emit pattern {
    name := $n
}`))
		require.NoError(t, err)
		res, err := Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip",
			Options{Extract: true, SourceManifest: FormatSourceManifest("doc", "inline")})
		require.NoError(t, err)
		require.NotNil(t, res.Extract)
		assert.Empty(t, res.Extract.Rows, "$n names nothing without an `as` clause")
	})
}

// positionedWalkFixture is a CONTAINS-only pdf-shaped graph whose edges carry a
// position on their Evidence, which validatorFixture's do not — `edge.position`
// is censused against the Evidence keys the loaded graph actually stamps, so a
// fixture with no Evidence would refuse the read before the walk ever ran.
func positionedWalkFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "d1", Type: "document", SymbolName: "Doc"},
			{Id: "s0", Type: "section", SymbolName: "One", Metadata: map[string]string{"level": "1"}},
			{Id: "s1", Type: "section", SymbolName: "Two", Metadata: map[string]string{"level": "1"}},
			{Id: "p0", Type: "paragraph", SymbolName: "para", Metadata: map[string]string{"page_first": "3"}},
		},
		edges: []*knowledgev1.Edge{
			ordEdge("d1", "s0", "0"),
			ordEdge("d1", "s1", "1"),
			// THE DEPTH-2 EDGE CARRIES A POSITION ITS PARENT'S DOES NOT, and that
			// difference is what makes the depth assertion discriminating. With "0"
			// here the depth-2 edge matched its depth-1 parent's, so an
			// implementation threading the depth-1 hop's edge down to every
			// descendant satisfied the assertion too — the leg read as if it
			// covered depth while covering only breadth. s0 has exactly one child,
			// so "2" introduces no sibling collision and the reading-order index's
			// ambiguity refusal is untouched.
			ordEdge("s0", "p0", "2"),
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
	require.NoError(t, err)
	return sv
}

// TestWalk_RowsCarryTheEdgeTheyWereReachedAlong gates the seam between the edge
// attribute reads and the walk rule.
//
// A walk IS an edge step, exactly as a traverse is, so a walked row must carry
// the edge it was reached along. Without it `edge.position` on a walked row is
// either refused as an illegal head or resolves to the empty string — a silent
// zero on precisely the class this validator exists to close.
func TestWalk_RowsCarryTheEdgeTheyWereReachedAlong(t *testing.T) {
	sv := positionedWalkFixture(t)

	extract := func(t *testing.T, body string) *ExtractResult {
		t.Helper()
		r, err := Parse([]byte(body))
		require.NoError(t, err, "the body must PARSE — a walk is an edge step, so `edge` is a legal head after one")
		res, err := Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip",
			Options{Extract: true, SourceManifest: FormatSourceManifest("doc", "inline")})
		require.NoError(t, err)
		require.NotNil(t, res.Extract)
		return res.Extract
	}

	t.Run("edge_position_reads_on_every_walked_row", func(t *testing.T) {
		ex := extract(t, `select document
walk CONTAINS
emit outline {
    name := node.symbol_name
    p := edge.position
}`)
		require.Len(t, ex.Rows, 3, "two sections and one paragraph")
		got := map[string]string{}
		for _, row := range ex.Rows {
			got[row.Fields["name"]] = row.Fields["p"]
		}
		assert.Equal(t, map[string]string{"One": "0", "Two": "1", "para": "2"}, got,
			"each walked row reads the position stamped on the edge it was reached along")
	})

	t.Run("edge_type_reads_on_every_walked_row", func(t *testing.T) {
		ex := extract(t, `select document
walk CONTAINS
emit outline {
    name := node.symbol_name
    t := edge.type
}`)
		require.Len(t, ex.Rows, 3)
		for _, row := range ex.Rows {
			assert.Equal(t, "CONTAINS", row.Fields["t"], "row %q", row.Fields["name"])
		}
	})

	// THE CONTROL, and it is what keeps the fix from being "make `edge` legal
	// everywhere": a select establishes rows that walked no edge, so the head is
	// dropped by scope.reset and the read is refused at PARSE time, with no
	// source graph consulted at all.
	t.Run("a_select_derived_row_has_no_edge_head", func(t *testing.T) {
		_, err := Parse([]byte(`select section
emit outline {
    name := node.symbol_name
    p := edge.position
}`))
		require.Error(t, err, "a select-derived row walked no edge")
		assert.Contains(t, err.Error(), "edge")
	})
}
