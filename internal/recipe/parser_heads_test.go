// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestParse_BareHeadValidation proves the validator reaches EVERY expression
// position, one subtest per site.
//
// Coverage is proved per site rather than counted: the whole justification for
// this validation is that a silently wrong head must break LOUDLY, so a
// validator reaching half its sites would ship a gate that is silent exactly
// where it was bought to be loud. A test asserting only that the site names
// appear somewhere would be satisfied by a comment listing them.
func TestParse_BareHeadValidation(t *testing.T) {
	// Each fixture plants a bad head at ONE site and nowhere else, so a failure
	// localizes to that site.
	badSites := []struct {
		name string
		body string
	}{
		{"select_where", "select section where page.symbol_name ~= /x/\nemit pattern {\n    name := section.symbol_name\n}"},
		{"filter_pred", "select section\nfilter page.symbol_name ~= /x/\nemit pattern {\n    name := section.symbol_name\n}"},
		{"bind_value", "select section\nbind $v := page.symbol_name\nemit pattern {\n    name := section.symbol_name\n}"},
		{"group_by", "select section\ngroup_by page.symbol_name\nemit pattern {\n    name := section.symbol_name\n}"},
		{"emit_field", "select section\nemit pattern {\n    name := page.symbol_name\n}"},
		{"lookup_id", "select section\nlookup pattern by page.symbol_name as $p\nemit pattern {\n    name := section.symbol_name\n}"},
		{"link_from", "select section\nemit pattern {\n    name := section.symbol_name\n} as $a\nlink page.symbol_name --[relates-to]--> $a"},
		{"link_to", "select section\nemit pattern {\n    name := section.symbol_name\n} as $a\nlink $a --[relates-to]--> page.symbol_name"},
		{"source_ref", "select section\nsource_ref page.id\nemit pattern {\n    name := section.symbol_name\n}"},
		// The recursion cases: a bad head nested inside a call argument, and
		// one on a regex left-hand side. Saved recipes write both shapes.
		{"func_arg", "select section\nemit pattern {\n    name := concat(section.symbol_name, page.symbol_name)\n}"},
		{"regex_lhs", "select section\nfilter page.symbol_name ~= /router/\nemit pattern {\n    name := section.symbol_name\n}"},
	}
	for _, tc := range badSites {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			require.Error(t, err, "a bad head at this site must be refused")
			assert.Contains(t, err.Error(), "page", "the error must name the offending head")
			assert.Contains(t, err.Error(), "section", "the error must list the legal set so the recipe can be repaired from it")
		})
	}

	// node is the universal row alias and is legal under a select of a
	// DIFFERENT type. Without its own case, someone tightening the legal set
	// later breaks eight in-repo fixtures with nothing going red first.
	t.Run("node_alias", func(t *testing.T) {
		body := "select section\ngroup_by node.metadata.family\nemit pattern {\n    name := section.symbol_name\n    keys := group.keys\n}"
		r, err := Parse([]byte(body))
		require.NoError(t, err, "node and group are legal heads under any select")
		require.NotNil(t, r)
	})

	// The OTHER half of the decision: the reversal is HEADS ONLY. A legal head
	// followed by a key nothing carries must parse cleanly AND evaluate to
	// empty, never error.
	t.Run("meta_soft", func(t *testing.T) {
		body := "select section\nemit pattern {\n    name := section.symbol_name\n    absent := section.metadata.no_such_key\n}"
		r, err := Parse([]byte(body))
		require.NoError(t, err, "an unknown metadata KEY stays fail-soft")

		// Parsing is only half of it — prove it evaluates to empty too.
		n := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Router"}
		sv := &sourceView{
			byID:   map[string]*knowledgev1.Node{"s1": n},
			byType: map[string][]*knowledgev1.Node{"section": {n}},
		}
		result, err := Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
		require.NoError(t, err)
		require.Len(t, result.Nodes, 1)
		assert.Equal(t, "Router", result.Nodes[0].SymbolName, "control: the legal head DID resolve")
		assert.Empty(t, result.Nodes[0].Metadata["absent"], "an unknown metadata key evaluates to empty")
	})

	// The positive half, in both directions, so a validator that accepted
	// everything could not pass this test.
	t.Run("legal_heads_accepted", func(t *testing.T) {
		for _, body := range []string{
			// The selected type itself.
			"select section\nemit pattern {\n    name := section.symbol_name\n}",
			// A traverse alias declared earlier.
			"select section\ntraverse contains out as $child\nemit pattern {\n    name := child.symbol_name\n}",
			// A var path, which is not a bare head at all.
			"select section\nbind $v := section.symbol_name\nemit pattern {\n    name := $v\n}",
		} {
			_, err := Parse([]byte(body))
			assert.NoError(t, err, "legal recipe refused: %s", body)
		}
	})

	t.Run("head_before_any_select", func(t *testing.T) {
		_, err := Parse([]byte("filter section.symbol_name ~= /x/\nselect section\nemit pattern {\n    name := section.symbol_name\n}"))
		require.Error(t, err, "a bare head before any select has no row to read from")
		assert.Contains(t, strings.ToLower(err.Error()), "select")
	})

	t.Run("alias_reset_by_second_select", func(t *testing.T) {
		// `child` is legal after the traverse, then a SECOND select resets the
		// legal set and it stops being legal.
		body := "select section\ntraverse contains out as $child\nselect paragraph\nemit pattern {\n    name := child.symbol_name\n}"
		_, err := Parse([]byte(body))
		require.Error(t, err, "a second select must reset the legal set")
		assert.Contains(t, err.Error(), "child")
	})
}
