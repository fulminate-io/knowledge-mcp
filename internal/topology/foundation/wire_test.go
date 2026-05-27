// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

	calls    int
	lastPlan *knowledgev1.QueryPlan
}

func (f *fakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.calls++
	q := req.GetQuery()
	f.lastPlan = q
	resp := &knowledgev1.ExecuteResponse{}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		resp.Edges = f.edges
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		resp.GraphNames = f.graphNames
	case q.GetById() != "":
		if n, ok := f.byID[q.GetById()]; ok {
			resp.Nodes = []*knowledgev1.Node{n}
		}
	default:
		resp.Nodes = f.nodes
	}
	return resp, nil
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
	if got := f.lastPlan.GetSelection().GetNodeTypes(); len(got) != 1 || got[0] != "function" {
		t.Fatalf("want plural-type selection [function], got %v", got)
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
	if f.calls != 1 {
		t.Fatalf("want exactly 1 Execute, got %d", f.calls)
	}
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
