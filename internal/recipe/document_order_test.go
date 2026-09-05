// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// document_order_test.go covers the reading-order index, the ordering contract
// on select and traverse, and the walk rule.
//
// EVERY FIXTURE HERE LISTS ITS NODES AND EDGES IN REVERSE DOCUMENT ORDER. That
// is the control: an implementation returning index or materialization order
// returns exactly the reverse of the expected answer rather than something that
// happens to look right.

// ordEdge builds a containment edge carrying a position on its Evidence, in the
// UPPER-CASE spelling both raw collectors emit. It sits beside
// interpret_render_test.go's posEdge rather than reusing it: that helper is
// pinned to the lower-case `contains` its own fixtures assert.
func ordEdge(from, to, pos string) *knowledgev1.Edge {
	return &knowledgev1.Edge{
		FromId: from, ToId: to, Type: "CONTAINS",
		Evidence: `{"position":"` + pos + `"}`,
	}
}

// docNode builds a document node carrying NO position metadata, so its order key
// can only come from the edge.
func docNode(id, nodeType string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: nodeType, SymbolName: id}
}

// pagedNode builds a SECTION node (type fixed, not a parameter: every case here
// is a section) carrying the two keys a collected page-based document stamps:
// `position` (the node-side order key) and `page_first`.
func pagedNode(id, position, page string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, Type: "section", SymbolName: id,
		Metadata: map[string]string{"position": position, "page_first": page},
	}
}

// shuffledDoc builds doc → s0(p00, p01), s1(p10), s2 and lists every node and
// every edge in REVERSE document order.
func shuffledDoc() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	nodes := []*knowledgev1.Node{
		docNode("s2", "section"),
		docNode("p10", "paragraph"),
		docNode("s1", "section"),
		docNode("p01", "paragraph"),
		docNode("p00", "paragraph"),
		docNode("s0", "section"),
		docNode("doc", "document"),
	}
	edges := []*knowledgev1.Edge{
		ordEdge("s1", "p10", "0"),
		ordEdge("s0", "p01", "1"),
		ordEdge("s0", "p00", "0"),
		ordEdge("doc", "s2", "2"),
		ordEdge("doc", "s1", "1"),
		ordEdge("doc", "s0", "0"),
	}
	return nodes, edges
}

// rowIDs projects a rowset down to its node ids, in order.
func rowIDs(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.NodeID)
	}
	return out
}

// selectIDs runs one select against a view and returns the resulting ids.
func selectIDs(t *testing.T, sv *sourceView, nodeType string) []string {
	t.Helper()
	env := newEnv()
	require.NoError(t, evalSelect(context.Background(), env, RuleSelect{NodeType: nodeType}, sv))
	return rowIDs(env.Rows)
}

func TestDocumentOrder_SelectIsReadingOrder(t *testing.T) {
	t.Run("sections", func(t *testing.T) {
		sv := renderView(shuffledDoc())
		assert.Equal(t, []string{"s0", "s1", "s2"}, selectIDs(t, sv, "section"))
	})

	// A SIBLING-INDEX SORT FAILS THIS ONE. p01 sits at position 1 under s0 and
	// p10 at position 0 under s1, so ordering by sibling index alone puts p10
	// ahead of p01 — two paragraphs that are strictly ordered in the document
	// and incomparable to a per-parent sort.
	t.Run("paragraphs_across_parents", func(t *testing.T) {
		sv := renderView(shuffledDoc())
		assert.Equal(t, []string{"p00", "p01", "p10"}, selectIDs(t, sv, "paragraph"))
	})

	// THE IDS AND THE GUARANTEE DISAGREE ON PURPOSE: "aaa" and "zzz" are touched
	// by no positioned edge and sort either side of the document root "doc", so
	// an implementation that ROOTS unpositioned nodes puts "aaa" ahead of the
	// whole document while one that leaves them UNRANKED sends both to the tail.
	t.Run("unpositioned_sort_last_even_when_their_ids_sort_first", func(t *testing.T) {
		nodes, edges := shuffledDoc()
		nodes = append(nodes, docNode("zzz", "section"), docNode("aaa", "section"))
		sv := renderView(nodes, edges)
		assert.Equal(t, []string{"s0", "s1", "s2", "aaa", "zzz"}, selectIDs(t, sv, "section"),
			"an unpositioned node follows every ordered node even when its id sorts first")
	})
}

func TestDocumentOrder_TraverseIsReadingOrder(t *testing.T) {
	sv := renderView(shuffledDoc())
	env := newEnv()
	require.NoError(t, evalSelect(context.Background(), env, RuleSelect{NodeType: "document"}, sv))
	require.NoError(t, evalTraverse(context.Background(), env,
		RuleTraverse{EdgeType: "CONTAINS", Direction: "out"}, sv))
	assert.Equal(t, []string{"s0", "s1", "s2"}, rowIDs(env.Rows))
}

// TestDocumentOrder_NodePositionBeatsEdgePosition makes the two carriers
// DISAGREE — node 0/1 against edge 5/2 — so a key reading only one of them
// returns the other's answer.
func TestDocumentOrder_NodePositionBeatsEdgePosition(t *testing.T) {
	edges := []*knowledgev1.Edge{
		ordEdge("root", "a", "5"),
		ordEdge("root", "b", "2"),
	}

	t.Run("node_wins", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("root", "document"),
			pagedNode("a", "0", "1"),
			pagedNode("b", "1", "2"),
		}
		assert.Equal(t, []string{"a", "b"}, selectIDs(t, renderView(nodes, edges), "section"))
	})

	// Strip the node keys and the SAME edges must flip the answer, which is what
	// a key that only ever reads one carrier cannot do.
	t.Run("edge_is_the_fallback", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("root", "document"),
			docNode("a", "section"),
			docNode("b", "section"),
		}
		assert.Equal(t, []string{"b", "a"}, selectIDs(t, renderView(nodes, edges), "section"))
	})
}

func TestDocumentOrder_RefusesAmbiguousPosition(t *testing.T) {
	t.Run("two_parents_are_both_named", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("doc", "document"),
			docNode("s1", "section"),
			docNode("s0", "section"),
			docNode("shared", "paragraph"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("s1", "shared", "0"),
			ordEdge("s0", "shared", "0"),
			ordEdge("doc", "s1", "1"),
			ordEdge("doc", "s0", "0"),
		}
		env := newEnv()
		err := evalSelect(context.Background(), env,
			RuleSelect{NodeType: "paragraph"}, renderView(nodes, edges))
		require.Error(t, err, "a coercing implementation returns [shared] here with no error")
		assert.Contains(t, err.Error(), `"shared"`, "the ambiguous node is named")
		assert.Contains(t, err.Error(), `"s0"`, "the first claiming parent is named")
		assert.Contains(t, err.Error(), `"s1"`, "the second claiming parent is named")
	})

	// A CROSS-REFERENCE EDGE COUNTS AS A SECOND POSITIONED PARENT, because the
	// order key is read from the TARGET NODE first and the node carries a position
	// whatever edge reached it. The REFERENCES edge below carries no position on
	// its own Evidence at all and is still admitted into the document relation.
	//
	// This is pinned rather than fixed: node-first precedence is what the ticket
	// asked for, and narrowing admission to edge-carried positions would undo it.
	// On the two collectors a recipe can run against, the shape does not occur —
	// the pdf emitter draws only CONTAINS, and the web emitter's REFERENCES edges
	// end on page nodes, which carry no position. A future collector that stamps
	// position on a node reachable by a second edge WILL hit this, and this
	// subtest is where that is written down.
	t.Run("a_cross_reference_edge_into_a_positioned_node_also_refuses", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("doc", "document"),
			pagedNode("s1", "1", "2"),
			pagedNode("s0", "0", "1"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("doc", "s0", "0"),
			ordEdge("doc", "s1", "1"),
			svEdge("s0", "s1", "REFERENCES"),
		}
		env := newEnv()
		err := evalSelect(context.Background(), env,
			RuleSelect{NodeType: "section"}, renderView(nodes, edges))
		require.Error(t, err, "s1 is claimed by doc and by s0's cross-reference")
		assert.Contains(t, err.Error(), `"s1"`)
		assert.Contains(t, err.Error(), `"doc"`)
		assert.Contains(t, err.Error(), `"s0"`,
			"the cross-referencing node is named as a claimant even though it is not a parent")
	})

	// A TRUE DUPLICATE IS NOT AMBIGUOUS. Two edges identical in parent, child
	// AND position say the same thing twice; there is one document position and
	// nothing to choose between. Counting positioned EDGES rather than distinct
	// claims reported this as `claimed by 2 positioned parents ("p", "p")` and
	// refused the run with a repair instruction naming an edge that is not a
	// second parent.
	//
	// LIVE CARRIER, checked rather than assumed: the github materializer draws
	// its lowercase `contains` family in ONE loop over the node slice, one edge
	// per (gh-root, file) pair, and those edges carry no Evidence at all — so the
	// same-parent duplicate does not arise there today. Whether a file node
	// carries a `position` key comes from the external populator and is NOT
	// traced here; if one does, that edge becomes a positioned claim, but still
	// only one per pair, which is the second-PARENT shape above rather than this
	// one.
	t.Run("an_identical_duplicate_edge_is_not_ambiguous", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("p", "document"),
			docNode("c1", "section"),
			docNode("c0", "section"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("p", "c0", "0"),
			ordEdge("p", "c0", "0"), // the same claim, twice
			ordEdge("p", "c1", "1"),
		}
		assert.Equal(t, []string{"c0", "c1"}, selectIDs(t, renderView(nodes, edges), "section"),
			"a duplicated claim is accepted and the order is unchanged")
	})

	// Two edges from ONE parent with DIFFERENT positions remain ambiguous: the
	// child genuinely has two document positions. The message must not call the
	// one parent two parents.
	t.Run("two_positions_from_one_parent_are_still_refused", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("p", "document"),
			docNode("c0", "section"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("p", "c0", "0"),
			ordEdge("p", "c0", "3"),
		}
		env := newEnv()
		err := evalSelect(context.Background(), env,
			RuleSelect{NodeType: "section"}, renderView(nodes, edges))
		require.Error(t, err)
		// THE FULL PHRASE, INCLUDING THE COUNT, and the positions asserted as the
		// rendered list rather than as bare digits: `assert.Contains(err, "0")` is
		// satisfied by the node id "c0" and would pass against a message naming no
		// position at all.
		assert.Contains(t, err.Error(), `claimed at 2 distinct positions by one parent "p"`,
			"the message names the real shape rather than inventing a second parent")
		assert.Contains(t, err.Error(), "with positions 0, 3,",
			"and names both positions, in numeric order")
		assert.NotContains(t, err.Error(), `("p", "p")`,
			"one parent is never reported twice")
	})

	// THREE EDGES FROM ONE PARENT, with positions whose STRING order differs from
	// their numeric order: a message built by sorting them as text renders
	// "10, 2, 3", which reads as a different set of positions than the graph
	// carries. The count is rendered from len(kept) rather than a hardcoded
	// "two", which this case is what proves.
	t.Run("three_positions_from_one_parent_are_named_in_numeric_order", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("p", "document"),
			docNode("c0", "section"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("p", "c0", "3"),
			ordEdge("p", "c0", "10"),
			ordEdge("p", "c0", "2"),
		}
		env := newEnv()
		err := evalSelect(context.Background(), env,
			RuleSelect{NodeType: "section"}, renderView(nodes, edges))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `claimed at 3 distinct positions by one parent "p"`,
			"the count is the number of distinct claims, not the literal two")
		assert.Contains(t, err.Error(), "with positions 2, 3, 10,",
			"numeric order, not the 10, 2, 3 a string sort produces")
	})

	// THE CASE THAT SEPARATES THE COUNT FROM THE NOUN, and the reason the noun is
	// "distinct positions" rather than "positioned edges". Every other fixture in
	// this test has one edge per distinct claim, so len(kept) and the raw edge
	// count coincide and either noun reads true. Here they do not: THREE edges,
	// two of them the identical claim (p, c0, 3), leave TWO distinct claims after
	// the dedupe above. A message rendering len(kept) under the word "edges" would
	// say two edges where the graph carries three — the same small untruth as the
	// hardcoded "two" that the count replaced, one level in.
	t.Run("a_duplicate_edge_and_a_differing_one_count_distinct_positions", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("p", "document"),
			docNode("c0", "section"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("p", "c0", "3"),
			ordEdge("p", "c0", "3"), // the same claim, twice
			ordEdge("p", "c0", "10"),
		}
		env := newEnv()
		err := evalSelect(context.Background(), env,
			RuleSelect{NodeType: "section"}, renderView(nodes, edges))
		require.Error(t, err, "two distinct positions from one parent are still ambiguous")
		assert.Contains(t, err.Error(), `claimed at 2 distinct positions by one parent "p"`,
			"the count is of distinct claims and the noun says so; three edges, two positions")
		assert.NotContains(t, err.Error(), "positioned edges",
			"the deduped count is never rendered under the raw-edge noun")
		assert.Contains(t, err.Error(), "with positions 3, 10,",
			"the duplicated position is named once, in numeric order")
	})

	t.Run("unambiguous_graph_is_not_refused", func(t *testing.T) {
		sv := renderView(shuffledDoc())
		env := newEnv()
		assert.NoError(t, evalSelect(context.Background(), env, RuleSelect{NodeType: "section"}, sv))
	})

	// THIS LEG IS A DEADLINE, NOT AN ASSERTION, and deliberately so: an
	// implementation with no ambiguity pre-pass does not fail an assertion here,
	// it never returns. c1 is re-entered from outside the cycle, which is what
	// makes it a two-positioned-parent node and therefore refusable.
	t.Run("cycle_reachable_from_a_root_is_refused_not_hung", func(t *testing.T) {
		nodes := []*knowledgev1.Node{
			docNode("c2", "section"),
			docNode("c1", "section"),
			docNode("r", "section"),
		}
		edges := []*knowledgev1.Edge{
			ordEdge("c2", "c1", "0"),
			ordEdge("c1", "c2", "0"),
			ordEdge("r", "c1", "0"),
		}
		sv := renderView(nodes, edges)
		done := make(chan error, 1)
		go func() {
			env := newEnv()
			done <- evalSelect(context.Background(), env, RuleSelect{NodeType: "section"}, sv)
		}()
		select {
		case err := <-done:
			require.Error(t, err, "a positioned cycle reachable from a root must be refused")
			assert.Contains(t, err.Error(), "ambiguous")
		case <-time.After(10 * time.Second):
			t.Fatal("the index neither refused nor terminated on a positioned cycle reachable from a root")
		}
	})
}

// walkDoc is the nested fixture the walk tests read: doc → S0(P00, P01),
// S1(P10), S2, with page_first rising down the document and every node and edge
// listed in REVERSE document order. Symbol names differ from node ids so a
// projection cannot accidentally agree with an id sort.
func walkDoc() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	n := func(id, symbol, nodeType, position, page string) *knowledgev1.Node {
		return &knowledgev1.Node{
			Id: id, Type: nodeType, SymbolName: symbol,
			Metadata: map[string]string{"position": position, "page_first": page},
		}
	}
	nodes := []*knowledgev1.Node{
		n("s2", "S2", "section", "2", "5"),
		n("p10", "P10", "paragraph", "0", "3"),
		n("s1", "S1", "section", "1", "3"),
		n("p01", "P01", "paragraph", "1", "2"),
		n("p00", "P00", "paragraph", "0", "1"),
		n("s0", "S0", "section", "0", "1"),
		n("doc", "DOC", "document", "", "1"),
	}
	edges := []*knowledgev1.Edge{
		ordEdge("s1", "p10", "0"),
		ordEdge("s0", "p01", "1"),
		ordEdge("s0", "p00", "0"),
		ordEdge("doc", "s2", "2"),
		ordEdge("doc", "s1", "1"),
		ordEdge("doc", "s0", "0"),
	}
	return nodes, edges
}

// TestWalk_InterleavesLevelsWithDepth is the rule's headline behaviour: one walk
// returns the whole subtree as the document reads, levels interleaved, rather
// than the level-by-level blocks a repeated traverse produces.
func TestWalk_InterleavesLevelsWithDepth(t *testing.T) {
	sv := renderView(walkDoc())
	env := newEnv()
	require.NoError(t, evalSelect(context.Background(), env, RuleSelect{NodeType: "document"}, sv))
	require.NoError(t, evalWalk(context.Background(), env, RuleWalk{EdgeType: "CONTAINS"}, sv))

	got := make([]string, 0, len(env.Rows))
	for _, row := range env.Rows {
		got = append(got, row.Node.SymbolName+"@"+row.Vars["walk.depth"])
	}
	assert.Equal(t, []string{"S0@1", "P00@2", "P01@2", "S1@1", "P10@2", "S2@1"}, got,
		"the starting row is not emitted and depth 1 is a direct child")

	assert.Equal(t, []string{"0", "0", "1", "1", "0", "2"}, walkPositions(env.Rows),
		"walk.position carries the child's own order key")

	// PAGE NUMBERS NEVER GO BACKWARDS. This is what rejects a level-by-level
	// expansion whose reading-order sort was dropped: its rows are in a valid
	// order for each level and wrong across levels.
	prev := 0
	for i, row := range env.Rows {
		page, err := strconv.Atoi(row.Node.Metadata["page_first"])
		require.NoError(t, err)
		require.GreaterOrEqual(t, page, prev,
			"row %d (%s) went backwards in the document", i, row.Node.SymbolName)
		prev = page
	}
}

// walkPositions projects each row's walk.position pseudo-variable.
func walkPositions(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Vars["walk.position"])
	}
	return out
}

// TestWalk_TerminatesOnAnUnpositionedCycle exercises the guard the reading-order
// index does NOT need. These edges carry no position at all, so they are not in
// the positioned relation and the index's ambiguity refusal never sees them —
// only the walk's own visited set stands between this fixture and an unbounded
// descent.
func TestWalk_TerminatesOnAnUnpositionedCycle(t *testing.T) {
	nodes := []*knowledgev1.Node{
		docNode("c", "section"),
		docNode("b", "section"),
		docNode("a", "section"),
	}
	edges := []*knowledgev1.Edge{
		svEdge("c", "a", "CONTAINS"),
		svEdge("b", "c", "CONTAINS"),
		svEdge("a", "b", "CONTAINS"),
	}
	sv := renderView(nodes, edges)

	done := make(chan []string, 1)
	go func() {
		env := newEnv()
		env.Rows = []Row{{NodeID: "a", Node: sv.byID["a"], Vars: map[string]string{}}}
		if err := evalWalk(context.Background(), env, RuleWalk{EdgeType: "CONTAINS"}, sv); err != nil {
			done <- nil
			return
		}
		done <- rowIDs(env.Rows)
	}()
	select {
	case ids := <-done:
		assert.Equal(t, []string{"b", "c"}, ids,
			"every node of the cycle is walked once and the starting row is not re-emitted")
	case <-time.After(10 * time.Second):
		t.Fatal("the walk neither terminated nor refused on a cycle of unpositioned edges")
	}
}

// TestWalk_HeadIsRefusedWithoutAWalkRule proves the pseudo-variable namespace is
// scoped to the rule that stamps it, with the passing control beside it so a
// blanket refusal cannot pass.
func TestWalk_HeadIsRefusedWithoutAWalkRule(t *testing.T) {
	const withoutWalk = `select section
emit pattern {
    name := section.symbol_name
    d := walk.depth
}`
	const withWalk = `select section
walk CONTAINS
emit pattern {
    name := node.symbol_name
    d := walk.depth
}`

	_, err := Parse([]byte(withoutWalk))
	require.Error(t, err, "`walk.depth` with no walk rule reads a namespace nothing stamped")
	assert.Contains(t, err.Error(), "walk")

	_, err = Parse([]byte(withWalk))
	assert.NoError(t, err, "the passing control: a walk rule makes its own namespace legal")
}
