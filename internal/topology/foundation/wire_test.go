// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// fakeCaller is a scripted GraphCaller that routes each inbound ExecuteRequest
// to a seeded carrier based on the plan shape, counts how many Execute calls
// were issued, and records the last plan for assertions. It lets the wire +
// adapter tests run without a real graph server.
type fakeCaller struct {
	nodes      []*knowledgev1.Node          // RETURN_MODE_NODES (browse / ids)
	byID       map[string]*knowledgev1.Node // ById lookups
	edges      []*knowledgev1.Edge          // RETURN_MODE_EDGES
	graphNames []*knowledgev1.GraphInfo     // RETURN_MODE_GRAPH_NAMES
	// edgesByPivot, when set, serves the edges incident to each REQUESTED pivot
	// instead of the flat f.edges carrier — the per-page answer a paged pivot
	// read needs. Nil (the zero value) keeps every other test's behavior.
	edgesByPivot map[string][]*knowledgev1.Edge

	calls     int
	lastPlan  *knowledgev1.QueryPlan
	lastPlans []*knowledgev1.QueryPlan
}

func (f *fakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.calls++
	q := req.GetQuery()
	f.lastPlan = q
	f.lastPlans = append(f.lastPlans, q)
	resp := &knowledgev1.ExecuteResponse{}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		if f.edgesByPivot != nil {
			for _, id := range q.GetIds() {
				resp.Edges = append(resp.Edges, f.edgesByPivot[id]...)
			}
			break
		}
		resp.Edges = f.edges
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		resp.GraphNames = f.graphNames
	case q.GetById() != "":
		if n, ok := f.byID[q.GetById()]; ok {
			resp.Nodes = []*knowledgev1.Node{n}
		}
	default:
		resp.Nodes = f.browse(q)
	}
	return resp, nil
}

// browse models the server's node-browse semantics over the seeded f.nodes,
// closely enough that the difference between a type-INDEX selection and a
// post-filter is observable — which is the whole point, since a fake that
// returns f.nodes verbatim is green whether or not the read is capped.
//
// An EMPTY Selection.NodeType applies NO type filter. That rule is first because
// it is load-bearing rather than a detail: the all-nodes reads (FetchAllNodes,
// and every adapter_test case that reaches this arm through NewGonumGraph)
// carry an empty Selection, and a fake treating empty as "match nothing" turns
// all of them red.
func (f *fakeCaller) browse(q *knowledgev1.QueryPlan) []*knowledgev1.Node {
	sel := q.GetSelection()
	out := make([]*knowledgev1.Node, 0, len(f.nodes))
	for _, n := range f.nodes {
		if t := sel.GetNodeType(); t != "" && n.GetType() != t {
			continue // singular type — an INDEX selection, applied before the cap
		}
		out = append(out, n)
	}

	// The keyset cursor: ids strictly greater than the cursor, ascending. Only
	// applied when after_id is PRESENT; absent, the seeded order is served so the
	// pre-existing tests keep their current meaning.
	if q.AfterId != nil {
		sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
		cursor := q.GetAfterId()
		if cursor != "" {
			kept := out[:0]
			for _, n := range out {
				if n.GetId() > cursor {
					kept = append(kept, n)
				}
			}
			out = kept
		}
	}

	if lim := int(q.GetLimit()); lim > 0 && len(out) > lim {
		out = out[:lim]
	}

	// The plural types key is a POST-FILTER applied AFTER the cap — reproducing
	// the real defect ordering, which is what made the old plural-arm read return
	// zero nodes on a graph holding more than a page of other types sorting first.
	if types := sel.GetNodeTypes(); len(types) > 0 {
		want := map[string]bool{}
		for _, t := range types {
			want[t] = true
		}
		kept := out[:0]
		for _, n := range out {
			if want[n.GetType()] {
				kept = append(kept, n)
			}
		}
		out = kept
	}
	return out
}

func node(id, typ string) *knowledgev1.Node {
	n := &knowledgev1.Node{}
	n.Id = id
	n.Type = typ
	return n
}

func edge(from, to string, weight float64) *knowledgev1.Edge {
	e := &knowledgev1.Edge{}
	e.FromId = from
	e.ToId = to
	e.Weight = weight
	return e
}

func TestFetchNodesByType(t *testing.T) {
	f := &fakeCaller{nodes: []*knowledgev1.Node{node("a", "function"), node("b", "function")}}
	got, err := FetchNodesByType(context.Background(), f, kgtypes.GraphCode, "repo", kgtypes.NodeType("function"))
	if err != nil {
		t.Fatalf("FetchNodesByType: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(got))
	}
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute, got %d", f.calls)
	}
	if got := f.lastPlan.GetSelection().GetNodeType(); got != "function" {
		t.Fatalf("want singular-type index selection function, got %q", got)
	}
	if got := f.lastPlan.GetSelection().GetNodeTypes(); len(got) != 0 {
		t.Fatalf("the plural post-filter arm must not be taken, got %v", got)
	}
}

// TestFetchNodesByType_HeterogeneousGraphUncapped is the ticket's correctness
// claim, reproduced rather than asserted: 30 nodes of another type sort BEFORE
// the 12 wanted ones, so the old plural-arm read capped to the first 10 by id —
// all of type "other" — and then post-filtered them all away, returning ZERO
// function nodes to the analyzer.
func TestFetchNodesByType_HeterogeneousGraphUncapped(t *testing.T) {
	var seeded []*knowledgev1.Node
	for i := range 30 {
		seeded = append(seeded, node(fmt.Sprintf("a%02d", i), "other"))
	}
	for i := range 12 {
		seeded = append(seeded, node(fmt.Sprintf("z%02d", i), "function"))
	}
	f := &fakeCaller{nodes: seeded}

	got, err := FetchNodesByType(context.Background(), f, kgtypes.GraphCode, "repo", kgtypes.NodeType("function"))
	if err != nil {
		t.Fatalf("FetchNodesByType: %v", err)
	}
	// The EXACT count, not merely non-emptiness: a partial fix returning one page
	// must not pass.
	if len(got) != 12 {
		t.Fatalf("want all 12 function nodes, got %d", len(got))
	}
	for _, n := range got {
		if n.GetType() != "function" {
			t.Fatalf("want only function nodes, got %s of type %s", n.GetId(), n.GetType())
		}
	}
}

func TestFetchAllNodes(t *testing.T) {
	f := &fakeCaller{nodes: []*knowledgev1.Node{node("a", "x"), node("b", "y"), node("c", "z")}}
	got, err := FetchAllNodes(context.Background(), f, kgtypes.GraphKnowledge, "")
	if err != nil {
		t.Fatalf("FetchAllNodes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(got))
	}
	// A corpus smaller than one page is a single SHORT page, which ends the drain.
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute for a sub-page corpus, got %d", f.calls)
	}
	if f.lastPlan.GetLimit() <= 0 {
		t.Fatalf("every page must carry an explicit positive limit, got %d", f.lastPlan.GetLimit())
	}
	if f.lastPlan.AfterId == nil {
		t.Fatalf("after_id must be SET on every page — presence is what selects the keyset browse")
	}

	t.Run("drains_every_page_of_a_multi_page_corpus", func(t *testing.T) {
		// Larger than one page and not a multiple of it, so the drain must issue
		// several full pages plus a short final one. A single-page fixture would
		// let an unpaged read pass this test unnoticed.
		const extra = 7
		total := paging.BrowsePageSize*2 + extra
		nodes := make([]*knowledgev1.Node, 0, total)
		for i := range total {
			nodes = append(nodes, node(fmt.Sprintf("n-%05d", i), "function"))
		}
		fp := &fakeCaller{nodes: nodes}

		out, err := FetchAllNodes(context.Background(), fp, kgtypes.GraphKnowledge, "")
		if err != nil {
			t.Fatalf("FetchAllNodes multi-page: %v", err)
		}
		if fp.calls < 2 {
			t.Fatalf("a corpus larger than one page must issue more than one Execute, got %d", fp.calls)
		}
		if len(out) != total {
			t.Fatalf("the drained union must be COMPLETE: want %d nodes, got %d", total, len(out))
		}
		for i, p := range fp.lastPlans {
			if p.GetLimit() != int32(paging.BrowsePageSize) {
				t.Fatalf("page %d limit = %d, want %d", i, p.GetLimit(), paging.BrowsePageSize)
			}
		}
	})
}

func TestFetchNodeByID(t *testing.T) {
	f := &fakeCaller{byID: map[string]*knowledgev1.Node{"a": node("a", "function")}}
	got, ok, err := FetchNodeByID(context.Background(), f, kgtypes.GraphCode, "repo", "a")
	if err != nil {
		t.Fatalf("FetchNodeByID: %v", err)
	}
	if !ok || got == nil || got.Id != "a" {
		t.Fatalf("want node a found, got ok=%v node=%v", ok, got)
	}
	// Absent ID → ok=false.
	_, ok, err = FetchNodeByID(context.Background(), f, kgtypes.GraphCode, "repo", "missing")
	if err != nil {
		t.Fatalf("FetchNodeByID missing: %v", err)
	}
	if ok {
		t.Fatalf("want ok=false for absent node")
	}
	if f.calls != 2 {
		t.Fatalf("want 2 Execute (one per lookup), got %d", f.calls)
	}
}

func TestFetchEdges(t *testing.T) {
	f := &fakeCaller{edges: []*knowledgev1.Edge{edge("a", "b", 2), edge("b", "c", 0)}}
	got, err := FetchEdges(context.Background(), f, kgtypes.GraphCode, "repo", []string{"a", "b", "c"}, nil)
	if err != nil {
		t.Fatalf("FetchEdges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 edges, got %d", len(got))
	}
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute, got %d", f.calls)
	}
	if f.lastPlan.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		t.Fatalf("want RETURN_MODE_EDGES, got %v", f.lastPlan.GetReturnMode())
	}
	// Empty ids → no call.
	f2 := &fakeCaller{}
	if _, err := FetchEdges(context.Background(), f2, kgtypes.GraphCode, "repo", nil, nil); err != nil {
		t.Fatalf("FetchEdges empty: %v", err)
	}
	if f2.calls != 0 {
		t.Fatalf("want 0 Execute for empty ids, got %d", f2.calls)
	}
}

// TestFetchAllEdges asserts the BOUNDED whole-graph edge read. The match-all
// plan it used to send — no pivot of any shape, one Execute, no limit — is
// retired: a request whose cost scales with the whole edge table is exactly the
// unbounded surface this read must no longer offer. What replaces it is a paged
// pivot union, so the assertions are that every page names its pivots, carries
// an explicit positive limit, and unions completely.
func TestFetchAllEdges(t *testing.T) {
	f := &fakeCaller{edges: []*knowledgev1.Edge{edge("a", "b", 2), edge("b", "c", 0)}}
	got, err := FetchAllEdges(context.Background(), f, kgtypes.GraphCode, "repo", []string{"a", "b", "c"}, nil)
	if err != nil {
		t.Fatalf("FetchAllEdges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 edges, got %d", len(got))
	}
	if f.lastPlan.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		t.Fatalf("want RETURN_MODE_EDGES, got %v", f.lastPlan.GetReturnMode())
	}
	if len(f.lastPlan.GetIds()) == 0 {
		t.Fatalf("the page must name its pivots, got %+v", f.lastPlan)
	}
	if f.lastPlan.GetLimit() <= 0 {
		t.Fatalf("the page must carry an EXPLICIT positive limit, got %d", f.lastPlan.GetLimit())
	}

	// An empty id set is the empty-graph shape: no read at all.
	fEmpty := &fakeCaller{edges: []*knowledgev1.Edge{edge("a", "b", 1)}}
	if _, err := FetchAllEdges(context.Background(), fEmpty, kgtypes.GraphCode, "repo", nil, nil); err != nil {
		t.Fatalf("FetchAllEdges empty: %v", err)
	}
	if fEmpty.calls != 0 {
		t.Fatalf("want 0 Execute for an empty id set, got %d", fEmpty.calls)
	}

	// An edge-type filter still rides every page.
	f2 := &fakeCaller{edges: []*knowledgev1.Edge{edge("a", "b", 1)}}
	if _, err := FetchAllEdges(context.Background(), f2, kgtypes.GraphCode, "repo", []string{"a"},
		[]kgtypes.EdgeType{kgtypes.EdgeCalls}); err != nil {
		t.Fatalf("FetchAllEdges typed: %v", err)
	}
	if got := f2.lastPlan.GetSelection().GetEdgeTypes(); len(got) != 1 || got[0] != string(kgtypes.EdgeCalls) {
		t.Fatalf("want edge_types [%s], got %v", kgtypes.EdgeCalls, got)
	}

	t.Run("drains_every_pivot_page_of_a_multi_page_id_set", func(t *testing.T) {
		// Larger than one page, so a single-page fixture cannot satisfy this.
		const extra = 10
		total := paging.EdgePivotPageSize + extra
		fp := &fakeCaller{edgesByPivot: map[string][]*knowledgev1.Edge{}}
		ids := make([]string, 0, total)
		for i := range total {
			id := fmt.Sprintf("n-%05d", i)
			ids = append(ids, id)
			fp.edgesByPivot[id] = []*knowledgev1.Edge{edge(id, id+"-t", 1)}
		}

		out, err := FetchAllEdges(context.Background(), fp, kgtypes.GraphCode, "repo", ids, nil)
		if err != nil {
			t.Fatalf("FetchAllEdges multi-page: %v", err)
		}
		if fp.calls < 2 {
			t.Fatalf("an id set larger than one page must issue more than one Execute, got %d", fp.calls)
		}
		for _, p := range fp.lastPlans {
			if len(p.GetIds()) > paging.EdgePivotPageSize {
				t.Fatalf("page carries %d pivots, over the bound %d", len(p.GetIds()), paging.EdgePivotPageSize)
			}
		}
		if len(out) != total {
			t.Fatalf("the paged union must be COMPLETE: want %d edges, got %d", total, len(out))
		}
	})

	t.Run("a_saturated_single_pivot_aborts_naming_the_pivot", func(t *testing.T) {
		// One pivot whose page comes back at the ceiling: no split is left, so a
		// short union would be a silent lie about a whole-graph read.
		const hot = "hot-pivot"
		saturated := make([]*knowledgev1.Edge, 0, engine.CorrelationsEdgeScanCap)
		for i := range engine.CorrelationsEdgeScanCap {
			saturated = append(saturated, edge(hot, fmt.Sprintf("t-%06d", i), 1))
		}
		fs := &fakeCaller{edgesByPivot: map[string][]*knowledgev1.Edge{hot: saturated}}

		out, err := FetchAllEdges(context.Background(), fs, kgtypes.GraphCode, "repo", []string{hot}, nil)
		if err == nil {
			t.Fatalf("a saturated single pivot must abort, got %d edges and no error", len(out))
		}
		if !strings.Contains(err.Error(), hot) {
			t.Fatalf("the error must name the offending pivot %q, got %v", hot, err)
		}
		if out != nil {
			t.Fatalf("no partial edge set may accompany the abort, got %d edges", len(out))
		}
	})
}

func TestFetchGraphNames(t *testing.T) {
	gi := &knowledgev1.GraphInfo{}
	gi.Name = "acct-1"
	f := &fakeCaller{graphNames: []*knowledgev1.GraphInfo{gi}}
	got, err := FetchGraphNames(context.Background(), f, kgtypes.GraphCloud)
	if err != nil {
		t.Fatalf("FetchGraphNames: %v", err)
	}
	if len(got) != 1 || got[0].GetName() != "acct-1" {
		t.Fatalf("want [acct-1], got %v", got)
	}
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute, got %d", f.calls)
	}
	if f.lastPlan.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		t.Fatalf("want RETURN_MODE_GRAPH_NAMES, got %v", f.lastPlan.GetReturnMode())
	}
}

func TestFetchKnowledgeFindings(t *testing.T) {
	f := &fakeCaller{nodes: []*knowledgev1.Node{node("f1", "finding")}}
	got, err := FetchKnowledgeFindings(context.Background(), f, "iam_escalation", "role-1")
	if err != nil {
		t.Fatalf("FetchKnowledgeFindings: %v", err)
	}
	if len(got) != 1 || got[0].Id != "f1" {
		t.Fatalf("want [f1], got %v", got)
	}
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute, got %d", f.calls)
	}
	if got := f.lastPlan.GetSelection().GetNodeType(); got != "finding" {
		t.Fatalf("want finding type-browse, got %q", got)
	}
	preds := f.lastPlan.GetSelection().GetMetadataPredicates()
	if len(preds) != 2 {
		t.Fatalf("want 2 metadata predicates (algorithm + primary_evidence), got %d", len(preds))
	}
}
