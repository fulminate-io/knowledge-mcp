// SPDX-License-Identifier: Apache-2.0

package render

// child_index_order_test.go covers the positioned-sibling ordering BuildChildIndex
// applies. THE LEGS ARE LABELED because a reader cannot otherwise tell which
// assertions pin NEW behavior and which characterize behavior that already held:
//
//   RED-FIRST (new behavior, each SEEN to fail at d60b2521 before the sort
//   existed):
//     PositionedEdgesSortAscending, MixedPositionedAndUnpositioned,
//     NodePositionWinsOverEdgePosition, EdgePositionUsedWhenNodeHasNone,
//     MalformedPositionIsUnpositioned, DiamondFollowsPosition
//   CHARACTERIZATION GUARD (behavior that must survive the sort unchanged, green
//   before and after):
//     UnpositionedEdgesKeepInputOrder, DanglingEdgeStillSkipped,
//     DiamondWithNoPositionsIsUnchanged, DoesNotMutateCallerEdges
//
// MalformedPositionIsUnpositioned IS RED-FIRST, not a characterization guard: its
// fixture puts the malformed carrier AHEAD of a real position, so before the sort
// existed it came out first and the test failed. It was mislabeled here until the
// red leg was actually run — which is the reason these labels are worth writing
// down and the reason a label is only trustworthy once its leg has been seen.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// positionedEdge builds a contains edge carrying a position on its Evidence, the
// shape both raw collectors stamp and the section builder mirrors.
func positionedEdge(from, to string, pos int) *knowledgev1.Edge {
	e := containsEdge(from, to)
	e.Evidence = `{"position":"` + itoa(pos) + `"}`
	return e
}

// evidenceEdge builds a contains edge with a verbatim Evidence string, for the
// malformed-carrier classes.
func evidenceEdge(from, to, evidence string) *knowledgev1.Edge {
	e := containsEdge(from, to)
	e.Evidence = evidence
	return e
}

// positionedNode builds a child node carrying a position on its metadata — the
// carrier that WINS when the two disagree.
func positionedNode(id string, pos int) *knowledgev1.Node {
	n := node(id)
	kgtypes.SetValue(n, "position", itoa(pos))
	return n
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func childIDs(children []*knowledgev1.Node) []string {
	out := make([]string, 0, len(children))
	for _, c := range children {
		out = append(out, c.Id)
	}
	return out
}

// RED-FIRST. R1-c(i): every edge positioned, arriving shuffled on the wire →
// ascending output.
func TestBuildChildIndex_PositionedEdgesSortAscending(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("s0"), node("s1"), node("s2")}
	edges := []*knowledgev1.Edge{
		positionedEdge("root", "s2", 2),
		positionedEdge("root", "s0", 0),
		positionedEdge("root", "s1", 1),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"s0", "s1", "s2"}, childIDs(childIndex["root"]))
}

// CHARACTERIZATION GUARD. R1-c(ii): NO edge positioned — today's phases, steps
// and criteria — output IDENTICAL to input order. This is the promise
// TestBuildChildIndex_PreservesEdgeOrder already makes; it is restated here
// because it is the clause of the settlement that keeps every existing tree
// bit-for-bit unchanged, and the sort is where it could break.
func TestBuildChildIndex_UnpositionedEdgesKeepInputOrder(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("a"), node("b"), node("c")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "c"),
		containsEdge("root", "a"),
		containsEdge("root", "b"),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"c", "a", "b"}, childIDs(childIndex["root"]))
}

// RED-FIRST. R1-c(iii): mixed → positioned first ascending, unpositioned after
// in input order.
func TestBuildChildIndex_MixedPositionedAndUnpositioned(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("p0"), node("p1"), node("u1"), node("u2")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "u1"),
		positionedEdge("root", "p1", 1),
		containsEdge("root", "u2"),
		positionedEdge("root", "p0", 0),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"p0", "p1", "u1", "u2"}, childIDs(childIndex["root"]),
		"positioned edges sort ahead of unpositioned ones, which keep their arrival order")
}

// RED-FIRST. R1-c(iv): a position on the NODE disagreeing with the position on
// the EDGE — the node wins. This is the precedence the raw collectors' own
// reading-order index applies, carried verbatim so one graph cannot order two
// ways.
func TestBuildChildIndex_NodePositionWinsOverEdgePosition(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), positionedNode("first", 0), positionedNode("second", 1)}
	edges := []*knowledgev1.Edge{
		// The EDGE carriers say the opposite of the NODE carriers.
		positionedEdge("root", "second", 0),
		positionedEdge("root", "first", 1),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"first", "second"}, childIDs(childIndex["root"]),
		"the node's own position metadata wins over the edge's evidence")
}

// RED-FIRST. R1-c(v): a position on the EDGE only is used.
func TestBuildChildIndex_EdgePositionUsedWhenNodeHasNone(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("a"), node("b")}
	edges := []*knowledgev1.Edge{
		positionedEdge("root", "b", 1),
		positionedEdge("root", "a", 0),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"a", "b"}, childIDs(childIndex["root"]))
}

// CHARACTERIZATION GUARD. R1-c(vi): every malformed position carrier reads as
// UNPOSITIONED, never as 0. Treating an unreadable key as position zero would
// hoist a garbage child to the front of every tree it appears in — and would
// break the unpositioned-edges-are-untouched clause the whole design rests on.
func TestBuildChildIndex_MalformedPositionIsUnpositioned(t *testing.T) {
	malformed := map[string]string{
		"non-numeric position": `{"position":"x"}`,
		"empty position":       `{"position":""}`,
		"absent evidence":      ``,
		"non-JSON evidence":    `position=0`,
		"wrong key":            `{"index":"0"}`,
		"float position":       `{"position":"0.5"}`,
	}
	for name, evidence := range malformed {
		t.Run(name, func(t *testing.T) {
			nodes := []*knowledgev1.Node{node("root"), node("bad"), node("good")}
			edges := []*knowledgev1.Edge{
				evidenceEdge("root", "bad", evidence),
				positionedEdge("root", "good", 7),
			}

			childIndex, _ := BuildChildIndex("root", nodes, edges)

			assert.Equal(t, []string{"good", "bad"}, childIDs(childIndex["root"]),
				"a malformed position must sort AFTER a real position 7, not ahead of it as a coerced 0")
		})
	}
}

// CHARACTERIZATION GUARD. R1-c(vii): an edge dangling to a node absent from the
// fetched set is still skipped, sorted or not.
func TestBuildChildIndex_DanglingEdgeStillSkipped(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("real")}
	edges := []*knowledgev1.Edge{
		positionedEdge("root", "missing", 0),
		positionedEdge("root", "real", 1),
	}

	childIndex, byID := BuildChildIndex("root", nodes, edges)

	require.Len(t, childIndex["root"], 1)
	assert.Equal(t, "real", childIndex["root"][0].Id)
	_, hasMissing := byID["missing"]
	assert.False(t, hasMissing)
}

// RED-FIRST. R1-c(viii), first half: a DIAMOND whose two contains edges are BOTH
// positioned. The child is deduped to exactly ONE parent, and the test names
// WHICH: attribution follows POSITION by definition. Asserting only that one
// parent got it would pass under either attribution and could not tell a correct
// sort from an inert one.
func TestBuildChildIndex_DiamondFollowsPosition(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("left"), node("right"), node("shared")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "left"),
		containsEdge("root", "right"),
		// Traversal order reaches `left` first; the POSITIONS say `right` is first.
		positionedEdge("left", "shared", 1),
		positionedEdge("right", "shared", 0),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Empty(t, childIDs(childIndex["left"]), "the higher-positioned claim loses")
	assert.Equal(t, []string{"shared"}, childIDs(childIndex["right"]),
		"attribution follows position: the position-0 edge claims the child, not the first edge in traversal order")

	// ONE-FIELD-DIFFERS CONTROL: the identical fixture with the two positions
	// SWAPPED must attach the child to the OTHER parent. Without it the assertion
	// above cannot be distinguished from one that passes under any input.
	swapped := []*knowledgev1.Edge{
		containsEdge("root", "left"),
		containsEdge("root", "right"),
		positionedEdge("left", "shared", 0),
		positionedEdge("right", "shared", 1),
	}
	swappedIndex, _ := BuildChildIndex("root", nodes, swapped)
	assert.Equal(t, []string{"shared"}, childIDs(swappedIndex["left"]))
	assert.Empty(t, childIDs(swappedIndex["right"]))
}

// CHARACTERIZATION GUARD. R1-c(viii), second half: a diamond with NEITHER edge
// positioned attributes exactly as it does today — the first edge in traversal
// order. This is the stable-sort clause, and it is what keeps every existing
// tree and the status-cascade partitioner unchanged.
func TestBuildChildIndex_DiamondWithNoPositionsIsUnchanged(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("left"), node("right"), node("shared")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "left"),
		containsEdge("root", "right"),
		containsEdge("left", "shared"),
		containsEdge("right", "shared"),
	}

	childIndex, _ := BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{"shared"}, childIDs(childIndex["left"]),
		"with no positions the first contains edge still wins, exactly as before the sort")
	assert.Empty(t, childIDs(childIndex["right"]))
}

// CHARACTERIZATION GUARD. BuildChildIndex must not REORDER THE CALLER'S SLICE.
// Its structureEdges argument is the traversal's own result, which callers reuse
// (intercept_query_plan_tree reads it again for its json branch), so sorting in
// place would silently rewrite a slice the caller still owns.
func TestBuildChildIndex_DoesNotMutateCallerEdges(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("s0"), node("s1")}
	edges := []*knowledgev1.Edge{
		positionedEdge("root", "s1", 1),
		positionedEdge("root", "s0", 0),
	}
	before := []string{edges[0].ToId, edges[1].ToId}

	BuildChildIndex("root", nodes, edges)

	assert.Equal(t, []string{edges[0].ToId, edges[1].ToId}, before,
		"the caller's edge slice is left in its arrival order")
}
