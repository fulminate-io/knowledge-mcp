// SPDX-License-Identifier: Apache-2.0

package render

// child_index_consumers_test.go covers the BuildChildIndex consumers in THIS
// package that no other test reaches, per what-to-test R1-e, plus R1-d.
//
// THE CONSUMER LIST IS NOT THE TREE-RENDERER CALLER LIST, and that difference is
// the whole reason the sort lives in BuildChildIndex. assembleTicket and
// assembleProjectContainer partition the child index themselves and never call
// the tree renderer for their own children; the json assemble walks the index
// directly with no topological sort. A sort applied inside RenderTreeFromIndex
// would leave all three unordered.
//
// THE CHILDREN ARE TYPED AS THE CONTAINERS' OWN CHILD TYPES, not as plan
// sections. The order key ranks by POSITION and never consults the node type, so
// asserting it on plans-under-a-ticket and tickets-under-a-project is asserting
// the property that actually holds — and it is what a raw web or pdf graph, the
// only writer that can produce these shapes today, actually presents.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// positionedChild builds a child node plus its positioned containment edge, with
// the position stamped on BOTH carriers the way every producer stamps it.
func positionedChild(parent, id, name string, nodeType kgtypes.NodeType, pos int) (*knowledgev1.Node, *knowledgev1.Edge) {
	n := &knowledgev1.Node{
		Id: id, SymbolName: name, Type: string(nodeType), Status: kgtypes.StatusActive,
	}
	kgtypes.SetValue(n, "position", strconv.Itoa(pos))
	e := containsEdge(parent, id)
	e.Evidence = `{"position":"` + strconv.Itoa(pos) + `"}`
	return n, e
}

// orderedNames reads the child index's order for one parent.
func orderedNames(childIndex map[string][]*knowledgev1.Node, parent string) []string {
	out := make([]string, 0, len(childIndex[parent]))
	for _, c := range childIndex[parent] {
		out = append(out, c.SymbolName)
	}
	return out
}

// R1-d. RenderTreeFromIndex renders children in CHILD INDEX ORDER when no
// depends-on edge exists among them.
//
// THE INDEX IS FED DELIBERATELY REVERSED, so the rendered order can only match
// the index if the renderer reads it; a renderer that re-derived an order from
// the nodes themselves would produce the sorted order and pass by accident.
//
// The second leg is the CONTROL that gives the first one meaning: with a
// depends-on chain over the same three nodes, the topological sort OVERRIDES the
// index order. That is why a section builder must emit no depends-on edge, and
// it is the failure mode that would otherwise be silent — a chained section list
// reorders with no error and no tell.
func TestRenderTreeFromIndex_FollowsChildIndexOrder(t *testing.T) {
	kids := []*knowledgev1.Node{
		{Id: "s2", SymbolName: "S2", Type: string(kgtypes.NodePhase)},
		{Id: "s1", SymbolName: "S1", Type: string(kgtypes.NodePhase)},
		{Id: "s0", SymbolName: "S0", Type: string(kgtypes.NodePhase)},
	}
	root := &knowledgev1.Node{Id: "root", SymbolName: "Root", Type: string(kgtypes.NodePlan)}
	childIndex := map[string][]*knowledgev1.Node{"root": kids}

	t.Run("no depends-on: the index order is the rendered order", func(t *testing.T) {
		var sb strings.Builder
		RenderTreeFromIndex(&sb, root, 0, 4, childIndex, map[string]string{}, nil)
		assert.Equal(t, []string{"S2", "S1", "S0"}, renderedOrder(sb.String(), []string{"S0", "S1", "S2"}))
	})

	t.Run("a depends-on chain OVERRIDES the index order", func(t *testing.T) {
		var sb strings.Builder
		RenderTreeFromIndex(&sb, root, 0, 4, childIndex,
			map[string]string{"s1": "s0", "s2": "s1"}, nil)
		assert.Equal(t, []string{"S0", "S1", "S2"}, renderedOrder(sb.String(), []string{"S0", "S1", "S2"}),
			"the topo-sort outranks the index — which is why a section carries no depends-on edge")
	})
}

// renderedOrder returns the wanted names in the order they first appear.
func renderedOrder(body string, want []string) []string {
	type hit struct {
		name string
		at   int
	}
	var hits []hit
	for _, n := range want {
		if at := strings.Index(body, n); at >= 0 {
			hits = append(hits, hit{n, at})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

// R1-e consumer 7: assembleTicket. It partitions childIndex[ticket] into plans
// and researches itself and never calls the tree renderer for that partition, so
// the ORDER a ticket lists its plans in comes from the index alone.
func TestAssembleTicket_OrdersPlansByPosition(t *testing.T) {
	ticket := &knowledgev1.Node{Id: "tkt", SymbolName: "the ticket", Type: string(kgtypes.NodeTicket)}
	nodes := []*knowledgev1.Node{ticket}
	var edges []*knowledgev1.Edge
	for _, p := range []struct {
		id, name string
		pos      int
	}{{"p-two", "Plan two", 2}, {"p-zero", "Plan zero", 0}, {"p-one", "Plan one", 1}} {
		n, e := positionedChild("tkt", p.id, p.name, kgtypes.NodePlan, p.pos)
		nodes = append(nodes, n)
		edges = append(edges, e)
	}
	childIndex, _ := BuildChildIndex("tkt", nodes, edges)
	assert.Equal(t, []string{"Plan zero", "Plan one", "Plan two"}, orderedNames(childIndex, "tkt"),
		"a ticket lists its plans in the index's order, which assembleTicket partitions without re-sorting")
}

// R1-e consumer 8: assembleProjectContainer, the same shape one level up — and
// the one whose `Progress: d/N tickets done` count reads the same partition.
func TestAssembleProjectContainer_OrdersTicketsByPosition(t *testing.T) {
	project := &knowledgev1.Node{Id: "prj", SymbolName: "the project", Type: string(kgtypes.NodeProject)}
	nodes := []*knowledgev1.Node{project}
	var edges []*knowledgev1.Edge
	for _, tk := range []struct {
		id, name string
		pos      int
	}{{"t-two", "Ticket two", 2}, {"t-zero", "Ticket zero", 0}, {"t-one", "Ticket one", 1}} {
		n, e := positionedChild("prj", tk.id, tk.name, kgtypes.NodeTicket, tk.pos)
		nodes = append(nodes, n)
		edges = append(edges, e)
	}
	childIndex, _ := BuildChildIndex("prj", nodes, edges)
	assert.Equal(t, []string{"Ticket zero", "Ticket one", "Ticket two"}, orderedNames(childIndex, "prj"),
		"a project lists its tickets in the index's order")
}

// R1-e consumer 9: the ordering holds at DEPTH on a nested tree, which is the
// shape the json assemble walks (depth 5) and the one a single-level assertion
// cannot reach. A sort applied only to the root's children would pass every test
// above and fail here.
func TestBuildChildIndex_OrderHoldsAtDepth(t *testing.T) {
	root := &knowledgev1.Node{Id: "root", SymbolName: "Root", Type: string(kgtypes.NodePlan)}
	nodes := []*knowledgev1.Node{root}
	var edges []*knowledgev1.Edge
	// Two levels, each seeded in reverse position order.
	for _, mid := range []struct {
		id, name string
		pos      int
	}{{"m-one", "Mid one", 1}, {"m-zero", "Mid zero", 0}} {
		n, e := positionedChild("root", mid.id, mid.name, kgtypes.NodePhase, mid.pos)
		nodes = append(nodes, n)
		edges = append(edges, e)
		for _, leaf := range []struct {
			suffix string
			pos    int
		}{{"-b", 1}, {"-a", 0}} {
			ln, le := positionedChild(mid.id, mid.id+leaf.suffix, mid.name+leaf.suffix, kgtypes.NodeStep, leaf.pos)
			nodes = append(nodes, ln)
			edges = append(edges, le)
		}
	}
	childIndex, _ := BuildChildIndex("root", nodes, edges)
	assert.Equal(t, []string{"Mid zero", "Mid one"}, orderedNames(childIndex, "root"))
	assert.Equal(t, []string{"Mid zero-a", "Mid zero-b"}, orderedNames(childIndex, "m-zero"),
		"the order holds at every level, not only under the root")
	assert.Equal(t, []string{"Mid one-a", "Mid one-b"}, orderedNames(childIndex, "m-one"))
}

// R1-e, the remaining three consumers (assembleResearch, assembleTestPlan,
// assembleInstruction): ONE shared regression rather than three ordering tests,
// because no section ever hangs under them. What they owe is that a SECTIONLESS
// tree renders exactly as it did before the sort existed.
//
// THE ASSERTION IS AGAINST A LITERAL, not against a second call of the same
// function: comparing a renderer to itself passes however the renderer changes.
// This literal is the rendered output at the tree before this change, captured
// by running the same fixture there.
func TestRenderTreeFromIndex_SectionlessTreeIsByteIdentical(t *testing.T) {
	root := &knowledgev1.Node{Id: "root", SymbolName: "Research", Type: string(kgtypes.NodeResearch), Status: "open"}
	q1 := &knowledgev1.Node{Id: "q1", SymbolName: "Q one", Type: string(kgtypes.NodeQuestion), Status: "open", Description: "the first question"}
	q2 := &knowledgev1.Node{Id: "q2", SymbolName: "Q two", Type: string(kgtypes.NodeQuestion), Status: "open", Description: "the second question"}
	nodes := []*knowledgev1.Node{root, q1, q2}
	edges := []*knowledgev1.Edge{containsEdge("root", "q1"), containsEdge("root", "q2")}

	childIndex, _ := BuildChildIndex("root", nodes, edges)
	var sb strings.Builder
	RenderTreeFromIndex(&sb, root, 0, 3, childIndex, map[string]string{}, nil)

	// Captured by running this exact fixture through RenderTreeFromIndex at
	// d60b2521, the tree before this change.
	const preChange = "Research (research) [open]\n" +
		"  ID: root\n" +
		"  Q one (question) [open]\n" +
		"    the first question\n" +
		"    ID: q1\n" +
		"  Q two (question) [open]\n" +
		"    the second question\n" +
		"    ID: q2\n"
	require.Equal(t, preChange, sb.String(),
		"a tree with no positioned edge and no annotation renders exactly the bytes it did before this change")
}
