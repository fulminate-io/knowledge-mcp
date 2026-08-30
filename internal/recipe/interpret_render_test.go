// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// interpret_render_test.go holds the render-builtin tests. It is a separate
// file from the expression-evaluator tests for the same reason the production
// code is: two whole graph walks with their per-case fixtures do not fit
// alongside the existing suite under this package's file-length ceiling. The
// shared helpers stay reachable because both files are package recipe.

// renderNode builds a node with an explicit body-bearing field.
func renderNode(id, nodeType, symbol, content string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: nodeType, SymbolName: symbol, Content: content}
}

// posEdge builds a contains edge carrying a position, the way both raw
// collectors stamp one.
func posEdge(from, to, pos string) *knowledgev1.Edge {
	return &knowledgev1.Edge{
		FromId: from, ToId: to, Type: "contains",
		Evidence: `{"position":"` + pos + `"}`,
	}
}

// renderView indexes nodes and edges into a sourceView the same way
// loadSourceView does, so a fixture only has to list them.
func renderView(nodes []*knowledgev1.Node, edges []*knowledgev1.Edge) *sourceView {
	sv := &sourceView{
		byID:     make(map[string]*knowledgev1.Node, len(nodes)),
		byType:   make(map[string][]*knowledgev1.Node),
		outEdges: make(map[string][]*knowledgev1.Edge),
		inEdges:  make(map[string][]*knowledgev1.Edge),
	}
	for _, n := range nodes {
		sv.byID[n.Id] = n
		sv.byType[n.Type] = append(sv.byType[n.Type], n)
	}
	for _, e := range edges {
		sv.outEdges[e.FromId] = append(sv.outEdges[e.FromId], e)
		sv.inEdges[e.ToId] = append(sv.inEdges[e.ToId], e)
	}
	return sv
}

// TestEvalExpr_HeadingPath covers the upward walk: root-first ordering, the
// empty-value skip, the depth bound, and the multi-parent tie-break.
//
// multi_parent has its own subtest because no realistic source graph produces
// it — both collectors attach each node to exactly one parent — so an
// implementation that followed every parent, or picked nondeterministically,
// would pass every other case here.
func TestEvalExpr_HeadingPath(t *testing.T) {
	t.Run("root_first_chain", func(t *testing.T) {
		sv := renderView(
			[]*knowledgev1.Node{
				renderNode("root", "section", "Guide", ""),
				renderNode("mid", "section", "Messaging", ""),
				renderNode("leaf", "paragraph", "", "the body text"),
			},
			[]*knowledgev1.Edge{
				posEdge("root", "mid", "0"),
				posEdge("mid", "leaf", "0"),
			},
		)
		row := &Row{NodeID: "leaf", Node: sv.byID["leaf"], Vars: map[string]string{}}
		// Root-first, and the leaf's OWN field is not part of its path.
		assert.Equal(t, "Guide > Messaging",
			mustEval(t, newEnv(), row, fn("heading_path", "contains", "symbol_name", " > "), sv))
	})

	t.Run("empty_ancestor_skipped", func(t *testing.T) {
		sv := renderView(
			[]*knowledgev1.Node{
				renderNode("root", "section", "Guide", ""),
				renderNode("mid", "section", "", ""), // no symbol_name
				renderNode("leaf", "paragraph", "", "body"),
			},
			[]*knowledgev1.Edge{
				posEdge("root", "mid", "0"),
				posEdge("mid", "leaf", "0"),
			},
		)
		row := &Row{NodeID: "leaf", Node: sv.byID["leaf"], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("heading_path", "contains", "symbol_name", " > "), sv)
		assert.Equal(t, "Guide", got)
		// A skipped value must not leave its separator behind.
		assert.NotContains(t, got, " > ")
	})

	t.Run("depth_bound", func(t *testing.T) {
		// A chain longer than the bound: the walk stops, and the returned path
		// therefore holds at most maxRenderDepth values.
		const chain = maxRenderDepth + 10
		nodes := []*knowledgev1.Node{}
		edges := []*knowledgev1.Edge{}
		for i := range chain {
			nodes = append(nodes, renderNode("n"+strconv.Itoa(i), "section", "S"+strconv.Itoa(i), ""))
			if i > 0 {
				edges = append(edges, posEdge("n"+strconv.Itoa(i-1), "n"+strconv.Itoa(i), "0"))
			}
		}
		sv := renderView(nodes, edges)
		leaf := "n" + strconv.Itoa(chain-1)
		row := &Row{NodeID: leaf, Node: sv.byID[leaf], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("heading_path", "contains", "symbol_name", "|"), sv)
		require.NotEmpty(t, got, "control: the walk must produce something, or the bound below is unreadable")
		assert.LessOrEqual(t, len(strings.Split(got, "|")), maxRenderDepth)
	})

	t.Run("multi_parent", func(t *testing.T) {
		// TWO incoming edges with DISTINCT parent values. The first in
		// materialization order wins, deterministically.
		sv := renderView(
			[]*knowledgev1.Node{
				renderNode("first", "section", "FirstParent", ""),
				renderNode("second", "section", "SecondParent", ""),
				renderNode("leaf", "paragraph", "", "body"),
			},
			[]*knowledgev1.Edge{
				posEdge("first", "leaf", "0"),
				posEdge("second", "leaf", "0"),
			},
		)
		row := &Row{NodeID: "leaf", Node: sv.byID["leaf"], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("heading_path", "contains", "symbol_name", " > "), sv)
		assert.Equal(t, "FirstParent", got)
		assert.NotContains(t, got, "SecondParent", "the walk must stop at one parent, not union them")
	})
}

// TestEvalExpr_SubtreeConcat covers the downward walk: ordered immediate
// children, the deeper walk that reaches a code block the flattened page body
// drops, the non-integer depth error, and cycle termination.
//
// cycle_terminates carries load a top-level pass line could not see: without
// the visited set this case HANGS the tool rather than failing it.
func TestEvalExpr_SubtreeConcat(t *testing.T) {
	// One document: a section with two ordered paragraphs, and a nested
	// subsection holding a code block.
	docView := func() *sourceView {
		return renderView(
			[]*knowledgev1.Node{
				renderNode("sec", "section", "Message Router", ""),
				renderNode("p0", "paragraph", "", "ZERO"),
				renderNode("p1", "paragraph", "", "ONE"),
				renderNode("sub", "section", "Nested", ""),
				renderNode("code", "code_block", "", "CODE"),
			},
			[]*knowledgev1.Edge{
				// Supplied out of order so document order is observable.
				posEdge("sec", "p1", "1"),
				posEdge("sec", "p0", "0"),
				posEdge("sec", "sub", "2"),
				posEdge("sub", "code", "0"),
			},
		)
	}

	t.Run("depth_one_ordered_children", func(t *testing.T) {
		sv := docView()
		row := &Row{NodeID: "sec", Node: sv.byID["sec"], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("subtree_concat", "contains", "body", "|", "1"), sv)
		// Depth one reaches the immediate children only, in document order.
		// The nested section contributes nothing because it has no body.
		assert.Equal(t, "ZERO|ONE", got)
		assert.NotContains(t, got, "CODE", "depth one must not descend into the nested section")
	})

	t.Run("nested_includes_code_block", func(t *testing.T) {
		sv := docView()
		row := &Row{NodeID: "sec", Node: sv.byID["sec"], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("subtree_concat", "contains", "body", "|", "3"), sv)
		// The code block is exactly what the collector's own flattened page
		// body drops, which is why the deeper walk exists.
		assert.Equal(t, "ZERO|ONE|CODE", got)
	})

	t.Run("unparseable_max_depth", func(t *testing.T) {
		sv := docView()
		row := &Row{NodeID: "sec", Node: sv.byID["sec"], Vars: map[string]string{}}
		_, err := evalExpr(context.Background(), newEnv(), row,
			fn("subtree_concat", "contains", "body", "|", "deep"), sv)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "subtree_concat")
		assert.Contains(t, err.Error(), `"deep"`, "the error must name the offending value")
	})

	t.Run("cycle_terminates", func(t *testing.T) {
		// A child that contains its own parent. Without the visited set this
		// recurses until the depth bound at every level; with it, the walk
		// simply ends.
		sv := renderView(
			[]*knowledgev1.Node{
				renderNode("a", "section", "A", "AAA"),
				renderNode("b", "section", "B", "BBB"),
			},
			[]*knowledgev1.Edge{
				posEdge("a", "b", "0"),
				posEdge("b", "a", "0"),
			},
		)
		row := &Row{NodeID: "a", Node: sv.byID["a"], Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("subtree_concat", "contains", "body", "|", "10"), sv)
		// Each node is visited at most once, so the cycle contributes B and
		// never revisits A.
		assert.Equal(t, "BBB", got)
	})
}
