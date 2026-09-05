// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// whereFixture is a THREE-LEVEL graph, because a two-level one cannot tell an
// ancestor walk that stops at depth 1 from one that walks transitively:
//
//	d1 (document) --CONTAINS--> s1 (section "Chapter One") --CONTAINS--> p1 (paragraph)
//	d1            --CONTAINS--> s2 (section "Appendix")    --CONTAINS--> p2 (paragraph)
func whereFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "d1", Type: "document", SymbolName: "Doc"},
			{Id: "s1", Type: "section", SymbolName: "Chapter One", Metadata: map[string]string{"level": "1"}},
			{Id: "s2", Type: "section", SymbolName: "Appendix"},
			{Id: "p1", Type: "paragraph", SymbolName: "para a", Content: "hello"},
			{Id: "p2", Type: "paragraph", SymbolName: "para b"},
		},
		edges: []*knowledgev1.Edge{
			svEdge("d1", "s1", "CONTAINS"),
			svEdge("d1", "s2", "CONTAINS"),
			svEdge("s1", "p1", "CONTAINS"),
			svEdge("s2", "p2", "CONTAINS"),
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
	require.NoError(t, err)
	return sv
}

// survivors runs a where-tree over every node in the fixture and returns the
// ids that matched, so each subtest asserts a SET rather than a count — an
// evaluator keeping the right NUMBER of the wrong rows still fails.
func survivors(t *testing.T, sv *sourceView, tree *WhereNode, vars map[string]string) []string {
	t.Helper()
	env := newEnv()
	require.NoError(t, compileWhereTree(tree, Position{Line: 1, Col: 1}, env.whereRegexes))
	ids := []string{"d1", "p1", "p2", "s1", "s2"} // sorted, so the result is stable
	var out []string
	for _, id := range ids {
		n, ok := sv.nodeByID(id)
		require.True(t, ok)
		rowVars := map[string]string{}
		maps.Copy(rowVars, vars)
		row := Row{NodeID: id, Node: n, Vars: rowVars}
		matched, err := evalWhereTree(context.Background(), env, &row, tree, sv)
		require.NoError(t, err)
		if matched {
			out = append(out, id)
		}
	}
	return out
}

func mustTree(t *testing.T, body string) *WhereNode {
	t.Helper()
	tree, err := ParseWhereTree([]byte(body), Position{Line: 1, Col: 1})
	require.NoError(t, err)
	return tree
}

func TestWhereTree_EvalLeaves(t *testing.T) {
	sv := whereFixture(t)

	t.Run("kind", func(t *testing.T) {
		assert.Equal(t, []string{"s1", "s2"},
			survivors(t, sv, mustTree(t, `{"kind":{"of":"node","is":"section"}}`), nil))
	})

	t.Run("kind_accepts_a_list", func(t *testing.T) {
		assert.Equal(t, []string{"p1", "p2", "s1", "s2"},
			survivors(t, sv, mustTree(t, `{"kind":{"of":"node","is":["section","paragraph"]}}`), nil))
	})

	t.Run("matches", func(t *testing.T) {
		assert.Equal(t, []string{"s1"},
			survivors(t, sv, mustTree(t, `{"matches":{"of":"node.symbol_name","regex":"^Chapter"}}`), nil))
	})

	t.Run("equals", func(t *testing.T) {
		assert.Equal(t, []string{"s2"},
			survivors(t, sv, mustTree(t, `{"equals":{"of":"node.symbol_name","value":"Appendix"}}`), nil))
	})

	t.Run("exists", func(t *testing.T) {
		// Only s1 stamps the key; s2 is the same TYPE and does not, which is what
		// makes this a read of the node rather than of the type.
		assert.Equal(t, []string{"s1"},
			survivors(t, sv, mustTree(t, `{"exists":{"of":"node.metadata.level"}}`), nil))
	})

	t.Run("exists_and_equals_empty_are_complements", func(t *testing.T) {
		// The DSL has no null, so these two partition the rows exactly.
		present := survivors(t, sv, mustTree(t, `{"exists":{"of":"node.metadata.level"}}`), nil)
		absent := survivors(t, sv, mustTree(t, `{"equals":{"of":"node.metadata.level","value":""}}`), nil)
		assert.Equal(t, []string{"s1"}, present)
		assert.Equal(t, []string{"d1", "p1", "p2", "s2"}, absent)
	})

	t.Run("ancestor_walks_incoming", func(t *testing.T) {
		// TRANSITIVE: p1's CONTAINS ancestors are s1 then d1. A walk that stopped
		// at depth 1 would return only s1's children.
		assert.Equal(t, []string{"p1", "p2", "s1", "s2"},
			survivors(t, sv, mustTree(t, `{"ancestor":{"edge":"CONTAINS","where":{"kind":{"of":"node","is":"document"}}}}`), nil))
	})

	t.Run("ancestor_at_depth_two", func(t *testing.T) {
		// Only the paragraphs have a SECTION ancestor; the sections' own ancestor
		// is the document.
		assert.Equal(t, []string{"p1", "p2"},
			survivors(t, sv, mustTree(t, `{"ancestor":{"edge":"CONTAINS","where":{"matches":{"of":"node.symbol_name","regex":"Chapter|Appendix"}}}}`), nil))
	})

	t.Run("descendant_walks_outgoing", func(t *testing.T) {
		// The other direction, exercised in its own right: a fixture read only
		// through the ancestor leaf leaves this code path unread while looking
		// like coverage.
		assert.Equal(t, []string{"d1", "s1"},
			survivors(t, sv, mustTree(t, `{"descendant":{"edge":"CONTAINS","where":{"equals":{"of":"node.content","value":"hello"}}}}`), nil))
	})

	t.Run("edge_type_is_exact", func(t *testing.T) {
		// The same walk against the lowercase spelling the graph does not carry
		// matches nothing — which is the SILENCE the validator turns into a
		// refusal, pinned here so the evaluator is not what changed.
		assert.Empty(t, survivors(t, sv, mustTree(t, `{"descendant":{"edge":"contains","where":{"kind":{"of":"node","is":"paragraph"}}}}`), nil))
	})

	t.Run("var_is_carried_into_the_sub_tree", func(t *testing.T) {
		// $want is bound on the OUTER row. If the synthetic neighbor row were
		// given fresh Vars, this resolves to "" and matches nothing.
		// d1 is in the set too: the walk is transitive, so the document reaches
		// para a through its section.
		got := survivors(t, sv,
			mustTree(t, `{"descendant":{"edge":"CONTAINS","where":{"equals":{"of":"node.symbol_name","value":"para a"}}}}`), nil)
		require.Equal(t, []string{"d1", "s1"}, got)

		withVar := survivors(t, sv,
			mustTree(t, `{"descendant":{"edge":"CONTAINS","where":{"exists":{"of":"$want"}}}}`),
			map[string]string{"want": "para a"})
		assert.Equal(t, []string{"d1", "s1", "s2"}, withVar,
			"every node with any CONTAINS descendant, because the bound var reads non-empty inside the sub-tree")
	})

	t.Run("composer_all", func(t *testing.T) {
		assert.Equal(t, []string{"s1"},
			survivors(t, sv, mustTree(t, `{"all":[
                {"kind":{"of":"node","is":"section"}},
                {"exists":{"of":"node.metadata.level"}}
            ]}`), nil))
	})

	t.Run("composer_any", func(t *testing.T) {
		assert.Equal(t, []string{"d1", "s1"},
			survivors(t, sv, mustTree(t, `{"any":[
                {"kind":{"of":"node","is":"document"}},
                {"exists":{"of":"node.metadata.level"}}
            ]}`), nil))
	})

	t.Run("composer_not", func(t *testing.T) {
		assert.Equal(t, []string{"d1", "p1", "p2"},
			survivors(t, sv, mustTree(t, `{"not":{"kind":{"of":"node","is":"section"}}}`), nil))
	})

	t.Run("composers_and_leaves_on_one_node_are_anded", func(t *testing.T) {
		assert.Equal(t, []string{"s2"},
			survivors(t, sv, mustTree(t, `{"kind":{"of":"node","is":"section"},"not":{"exists":{"of":"node.metadata.level"}}}`), nil))
	})
}

// TestWhereTree_UnresolvableOfIsAnError is the aperture separating the correct
// evaluator from the plausible one that returns FALSE for an unreadable `of`.
// The plausible one drops every row silently, which is the class this grammar
// replaced.
func TestWhereTree_UnresolvableOfIsAnError(t *testing.T) {
	sv := whereFixture(t)
	env := newEnv()

	t.Run("empty_of", func(t *testing.T) {
		n, _ := sv.nodeByID("s1")
		row := Row{NodeID: "s1", Node: n, Vars: map[string]string{}}
		tree := mustTree(t, `{"equals":{"of":"","value":"x"}}`)
		_, err := evalWhereTree(context.Background(), env, &row, tree, sv)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty `of`")
	})

	t.Run("no_row_to_read_from", func(t *testing.T) {
		tree := mustTree(t, `{"matches":{"of":"node.symbol_name","regex":"x"}}`)
		require.NoError(t, compileWhereTree(tree, Position{Line: 1, Col: 1}, env.whereRegexes))
		_, err := evalWhereTree(context.Background(), env, nil, tree, sv)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no row to read it from")
	})
}

// TestWhereTree_UncompiledMatchesLeafIsAValidatorBug is the catcher for the
// never-compiled-here invariant. A lazy `if compiled == nil { compile }` in the
// evaluator returns identical answers on every input, so no correctness test
// can see it; requiring the error is what makes its ABSENCE enforceable.
func TestWhereTree_UncompiledMatchesLeafIsAValidatorBug(t *testing.T) {
	sv := whereFixture(t)
	env := newEnv()
	n, _ := sv.nodeByID("s1")
	row := Row{NodeID: "s1", Node: n, Vars: map[string]string{}}

	// Deliberately NOT compiled: this is the state the validator is supposed to
	// have removed before any row existed.
	tree := mustTree(t, `{"matches":{"of":"node.symbol_name","regex":"^Chapter"}}`)
	_, err := evalWhereTree(context.Background(), env, &row, tree, sv)
	require.Error(t, err, "an uncompiled leaf must not be repaired in place")
	assert.Contains(t, err.Error(), "validator bug")
	assert.Contains(t, err.Error(), "before the row loop")

	// And the same leaf, compiled as the validator would, evaluates normally —
	// so the error above is about the missing compile, not about the leaf.
	require.NoError(t, compileWhereTree(tree, Position{Line: 1, Col: 1}, env.whereRegexes))
	matched, err := evalWhereTree(context.Background(), env, &row, tree, sv)
	require.NoError(t, err)
	assert.True(t, matched)
}

// TestWhereTree_UnresolvedCompareLeafIsAValidatorBug pins the compare leaf's
// evaluator behaviour on the same terms as its matches sibling: a leaf missing
// from the run's resolved map is reported as the validator bug it is, never
// lazily re-resolved and never answered with a silent false.
//
// THE EMPTY whereCompares MAP IS THE SUBJECT. newEnv builds that map empty and
// nothing here populates it, so the leaf the evaluator is handed is exactly the
// unresolved one. A lazy-resolve fallback of the shape
// `if _, ok := …; !ok { resolve }` would return an ANSWER here instead of an
// error, and would return identical answers to the correct implementation on
// every other input — which is what makes the absence of that fallback
// enforceable here rather than aspirational.
func TestWhereTree_UnresolvedCompareLeafIsAValidatorBug(t *testing.T) {
	sv := whereFixture(t)
	env := newEnv()
	n, _ := sv.nodeByID("s1")
	row := Row{NodeID: "s1", Node: n, Vars: map[string]string{}}

	tree := mustTree(t, `{"compare":{"of":"node.metadata.level","op":"gt","value":"0"}}`)
	_, err := evalWhereTree(context.Background(), env, &row, tree, sv)
	require.Error(t, err, "an unresolved leaf must not be resolved in place")
	assert.Contains(t, err.Error(), "validator bug")
	assert.Contains(t, err.Error(), "before the row loop")

	// And the same leaf, resolved as the validator would, evaluates normally — so
	// the error above is about the missing resolution, not about the leaf.
	env.whereCompares[tree.Compare] = compareResolution{
		op: knowledgev1.MetadataPredicate_OP_GT, value: 0,
	}
	matched, err := evalWhereTree(context.Background(), env, &row, tree, sv)
	require.NoError(t, err)
	assert.True(t, matched)
}

// TestWhereTree_RegexCompiledOncePerRun is the third PERFORMANCE observable: a
// literal regex is compiled ONCE per run by the validator's compile pass, not
// once per row.
//
// STATED LIMIT, measured rather than assumed: this test alone does NOT catch a
// lazy-compile fallback in the evaluator, because the leaf is compiled at
// validation first so the fallback never fires on this path. Its partner is
// TestWhereTree_UncompiledMatchesLeafIsAValidatorBug, which requires the
// fallback branch to be ABSENT. The pair is not redundant, and recording that is
// what stops one of them being dropped later.
func TestWhereTree_RegexCompiledOncePerRun(t *testing.T) {
	sv := whereFixture(t)
	tree := mustTree(t, `{"matches":{"of":"node.symbol_name","regex":"^(Chapter|Appendix|para)"}}`)
	env := newEnv()

	before := regexCompiles.Load()
	require.NoError(t, compileWhereTree(tree, Position{Line: 1, Col: 1}, env.whereRegexes))
	afterCompilePass := regexCompiles.Load()
	require.Equal(t, int64(1), afterCompilePass-before, "the pass compiles the leaf exactly once")

	// Now drive many rows through that one leaf. A per-row compile would move the
	// counter by the row count.
	n, _ := sv.nodeByID("s1")
	for range 500 {
		row := Row{NodeID: "s1", Node: n, Vars: map[string]string{}}
		_, err := evalWhereTree(context.Background(), env, &row, tree, sv)
		require.NoError(t, err)
	}
	assert.Equal(t, afterCompilePass, regexCompiles.Load(),
		"500 evaluations compile nothing: the evaluator never compiles")
}
