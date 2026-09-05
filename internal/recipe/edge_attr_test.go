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

// evEdge builds an edge carrying Evidence, beside the landed svEdge, which
// deliberately carries none — an edge with no Evidence is the ordinary case on
// most graphs and svEdge stays the helper for it.
func evEdge(from, to, typ, evidence string) *knowledgev1.Edge {
	return &knowledgev1.Edge{FromId: from, ToId: to, Type: typ, Evidence: evidence}
}

// edgeFixture carries THREE EDGE POPULATIONS, because the edge reader has to
// tell them apart and a fixture with one of them cannot show that it does:
//
//   - CONTAINS edges stamped position 0 / 1 / 5, the shape both raw collectors
//     write. The gaps are deliberate: a threshold at 2 selects a proper subset,
//     so an implementation reading the wrong edge selects a different set rather
//     than the same one.
//   - ONE CONTAINS edge whose Evidence is the treesitter collector's OPAQUE
//     `import:fmt:3` form. It carries no keys at all, so it reads EMPTY and does
//     not error, and it contributes nothing to the census. That is the
//     false-predicate half of the absent-value rule on a real corpus shape.
//   - A REFERENCES edge carrying the web collector's rel/url payload, which is
//     what makes `edge.type` and `edge.rel` two DIFFERENT values one word apart
//     and therefore what gates the locked spelling.
//
// b0 also has its own child along a position-7 CONTAINS edge, so a walk leaf's
// sub-tree has an edge of its own to bind that differs from the outer row's.
func edgeFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "d1", Type: "document", SymbolName: "Doc"},
			{Id: "b0", Type: "block", SymbolName: "first"},
			{Id: "b1", Type: "block", SymbolName: "second"},
			{Id: "b5", Type: "block", SymbolName: "sixth"},
			{Id: "b9", Type: "block", SymbolName: "opaque"},
			{Id: "c0", Type: "block", SymbolName: "nested"},
			{Id: "t1", Type: "block", SymbolName: "linked"},
		},
		edges: []*knowledgev1.Edge{
			evEdge("d1", "b0", "CONTAINS", `{"position":"0"}`),
			evEdge("d1", "b1", "CONTAINS", `{"position":"1"}`),
			evEdge("d1", "b5", "CONTAINS", `{"position":"5"}`),
			evEdge("d1", "b9", "CONTAINS", "import:fmt:3"),
			evEdge("b0", "c0", "CONTAINS", `{"position":"7"}`),
			evEdge("b0", "t1", "REFERENCES", `{"rel":"external","url":"https://example.com/x"}`),
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphWebRaw, "eip")
	require.NoError(t, err)
	return sv
}

// edgeEmit runs a body and returns the emitted nodes, so a subtest asserts the
// VALUES a recipe author would see rather than an internal call's return.
func edgeEmit(t *testing.T, sv *sourceView, body string) []*knowledgev1.Node {
	t.Helper()
	r, err := Parse([]byte(body))
	require.NoError(t, err, "the body must PARSE — this subtest is about reads, not grammar")
	res, err := Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)
	return res.Nodes
}

// edgeNames renders the emitted node names as a sorted-by-emission set, so a
// subtest asserts WHICH rows survived rather than how many.
func edgeNames(nodes []*knowledgev1.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.SymbolName)
	}
	return out
}

func TestEdgeAttributeReads_Behaviour(t *testing.T) {
	sv := edgeFixture(t)

	t.Run("reads_the_traversed_edge", func(t *testing.T) {
		nodes := edgeEmit(t, sv, `select document
traverse CONTAINS out as $b
emit block {
    name := node.symbol_name
    et := edge.type
    pos := edge.position
}`)
		require.Len(t, nodes, 4, "every traversed row emits, including the opaque-evidence one")
		got := map[string][2]string{}
		for _, n := range nodes {
			got[n.SymbolName] = [2]string{n.Metadata["et"], n.Metadata["pos"]}
		}
		assert.Equal(t, [2]string{"CONTAINS", "0"}, got["first"])
		assert.Equal(t, [2]string{"CONTAINS", "1"}, got["second"])
		assert.Equal(t, [2]string{"CONTAINS", "5"}, got["sixth"])
		// THE OPAQUE-EVIDENCE ROW. Its Evidence is `import:fmt:3`, which carries
		// no keys — so the position reads EMPTY and the run continues. An
		// implementation treating an undecodable blob as a malformed map would
		// abort here, and one erroring on the missing key would abort too.
		assert.Equal(t, [2]string{"CONTAINS", ""}, got["opaque"],
			"an opaque Evidence reads empty and is not an error")
	})

	t.Run("edge_type_and_the_evidence_rel_key_are_different_values", func(t *testing.T) {
		// THE RENAME'S OWN GATE. All three spellings are read off ONE traversed
		// REFERENCES edge in a single emit. `rel` is a real Evidence key the web
		// collector stamps with "external"; the edge's own type is REFERENCES. An
		// implementation that kept the edge type under `rel` answers REFERENCES
		// for all three and reds here.
		nodes := edgeEmit(t, sv, `select block
traverse REFERENCES out as $r
emit link {
    name := node.symbol_name
    et := edge.type
    sugar := edge.rel
    explicit := edge.evidence.rel
}`)
		require.Len(t, nodes, 1)
		n := nodes[0]
		assert.Equal(t, "REFERENCES", n.Metadata["et"], "edge.type is the edge's OWN type")
		assert.Equal(t, "external", n.Metadata["sugar"], "edge.rel is the Evidence key of that name")
		assert.Equal(t, "external", n.Metadata["explicit"], "and edge.evidence.rel is the same key")
		assert.NotEqual(t, n.Metadata["et"], n.Metadata["sugar"],
			"the two spellings must not answer the same value — that is the collision the rename removed")
	})

	t.Run("compare_over_the_stamped_position", func(t *testing.T) {
		// THE TICKET'S OWN WORKED EXAMPLE: a numeric compare over the position the
		// contains-edge stamp carries. b5 is above the threshold; the
		// opaque-evidence row reads empty and does NOT match, rather than being
		// coerced to zero into the `lt` set.
		nodes := edgeEmit(t, sv, `select document
traverse CONTAINS out as $b
filter {"compare": {"of": "edge.position", "op": "lt", "value": "2"}}
emit block {
    name := node.symbol_name
}`)
		assert.Equal(t, []string{"first", "second"}, edgeNames(nodes))
	})

	t.Run("the_walk_leaf_binds_its_own_edge", func(t *testing.T) {
		// INSIDE a walk sub-tree, `edge` names the WALKED edge, not the outer
		// traverse's. b0 was reached along a position-0 edge and has a child
		// reached along a position-7 one; only the walked binding answers 7. An
		// implementation carrying the outer row's edge into the candidate row
		// answers 0 here and emits nothing.
		nodes := edgeEmit(t, sv, `select document
traverse CONTAINS out as $b
filter {"descendant": {"edge": "CONTAINS", "where": {"compare": {"of": "edge.position", "op": "eq", "value": "7"}}}}
emit block {
    name := node.symbol_name
}`)
		assert.Equal(t, []string{"first"}, edgeNames(nodes),
			"only the row whose WALKED edge carries position 7 survives")
	})

	t.Run("select_scoped_edge_read_is_a_parse_error", func(t *testing.T) {
		// HEAD LEGALITY IS DECIDABLE FROM THE RECIPE TEXT ALONE, so this is
		// refused by Parse with no source graph at all. A select establishes rows
		// that walked no edge, and scope.reset dropping the head is what refuses
		// it — which is why edgeHead is NOT in universalHeads.
		_, err := Parse([]byte(`select block
filter {"compare": {"of": "edge.position", "op": "lt", "value": "2"}}
emit block {
    name := node.symbol_name
}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown field head "edge"`)
		assert.Contains(t, err.Error(), "legal heads here are")
	})

	t.Run("refuses_an_attribute_the_edge_does_not_carry", func(t *testing.T) {
		// `weight` is a real Edge STRUCT field this leaf does not expose, which is
		// the read an author is likeliest to try. The refusal therefore names the
		// edge's own fields as well as the observed Evidence keys — a message
		// listing only the latter would never mention edge.type.
		msg := refusalFor(t, sv, `select document
traverse CONTAINS out as $b
emit block {
    name := edge.weight
}`)
		assert.Contains(t, msg, `"weight"`, "the offending attribute")
		assert.Contains(t, msg, "edge evidence key")
		assert.Contains(t, msg, "edge.type", "the edge's own fields are named too")
		assert.Contains(t, msg, "before the walk")
	})

	t.Run("refuses_a_near_missed_evidence_key", func(t *testing.T) {
		// The offending spelling doubles a letter rather than transposing two,
		// because the repo's misspell hook auto-corrects a transposed `position`
		// in place and would silently rewrite this subtest's subject into the
		// correct key — which would leave the test passing vacuously against a
		// key the graph does carry.
		msg := refusalFor(t, sv, `select document
traverse CONTAINS out as $b
emit block {
    name := edge.positionn
}`)
		assert.Contains(t, msg, `"positionn"`, "the offending key")
		assert.Contains(t, msg, `did you mean "position"?`)
		assert.Contains(t, msg, `"position", "rel", "url"`, "the observed evidence vocabulary")
	})

	t.Run("refuses_a_bare_edge_head_even_on_an_empty_rowset", func(t *testing.T) {
		// THE GATE ON THE ORDERING INSIDE checkFieldPath, and its subject is
		// BEHAVIORAL rather than structural: a bare `edge` read is refused BEFORE
		// THE WALK even when the rowset is empty by the time the read would run.
		//
		// The filter below admits nothing, so under an implementation whose length
		// guard sits ABOVE the bare-edge branch there is no row to fail at and the
		// run returns rows=0 with NO refusal — the silent zero the whole pre-walk
		// layer exists to end. The sibling leg above cannot see that: it has rows,
		// so the wrong order still errors there, just later and without a position.
		msg := refusalFor(t, sv, `select document
traverse CONTAINS out as $b
filter {"equals": {"of": "node.symbol_name", "value": "no-such-node"}}
emit r {
    name := node.symbol_name
    v := edge
}`)
		assert.Contains(t, msg, "refused before the walk",
			"an empty rowset must not swallow the refusal")
		assert.Contains(t, msg, "names no edge attribute")
	})

	t.Run("refuses_a_bare_edge_head", func(t *testing.T) {
		// A bare NODE head is legal — `section` alone reads the row's name — so
		// the length guard that admits it would send a bare `edge` to the node
		// read and answer a symbol name for a question about an edge.
		msg := refusalFor(t, sv, `select document
traverse CONTAINS out as $b
emit block {
    name := edge
}`)
		assert.Contains(t, msg, "names no edge attribute")
		assert.Contains(t, msg, "edge.type")
		assert.Contains(t, msg, "evidence")
	})
}

// TestEdgeCensus_RidesTheSameWalk is the census half of the PERFORMANCE
// observable: the fourth vocabulary is collected on the walk buildCensus already
// makes, not on a second one.
//
// TWO LEGS, because the walk counter ALONE cannot catch the violation. An
// implementation collecting the evidence keys in its own `range sv.outEdges`
// inside buildCensus still reads censusWalks == 1, since that counter is
// incremented once per build. The SET leg is what separates them — and it is
// also the known-positive the counter leg needs: a census that never collected
// anything would read 1 walk and an EMPTY set, which the equality below rejects.
func TestEdgeCensus_RidesTheSameWalk(t *testing.T) {
	sv := edgeFixture(t)
	require.Equal(t, 0, sv.censusWalks, "the census is not built until it is asked for")

	c := sv.census()
	sv.census()
	sv.census()
	assert.Equal(t, 1, sv.censusWalks, "three asks, one walk — the fourth vocabulary added none")

	// THE OPAQUE EDGE CONTRIBUTES NOTHING AND IS NOT A FAILURE. `import:fmt:3`
	// decodes to nil, so a census treating an undecodable blob as malformed reds
	// on this set rather than passing quietly.
	assert.Equal(t, []string{"position", "rel", "url"}, c.edgeEvidenceKeys)
	assert.Equal(t, []string{"CONTAINS", "REFERENCES"}, c.edgeTypes,
		"the edge-type vocabulary is unchanged by the fourth one riding the same loop")
}
