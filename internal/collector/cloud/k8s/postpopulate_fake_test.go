// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"slices"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// k8sFake is the package-local stateful postpopulate.GraphCaller used by the
// re-homed K8s PostPopulate integration tests. It replaces the real graph engine the
// resolvers used to take: nodes are seeded per per-account graph (keyed by
// Target.Account), create_batch mutations are ingested into the per-graph node +
// edge set, and browse / edge / graph-name reads are served back so a test can
// seed → run the resolver → assert on the captured edges. Routing is by
// Target.Account (a write/read with an empty account is a miss), so a test that
// asserts the write landed in the right per-account graph passes only when the
// resolver routed by Account (the FUL-288 selector-field invariant).
type k8sFake struct {
	// per-account graph state.
	nodes map[string]map[string]*knowledgev1.Node // account → id → node
	edges map[string][]knowledgev1.Edge           // account → edges
}

func newK8sFake() *k8sFake {
	return &k8sFake{
		nodes: map[string]map[string]*knowledgev1.Node{},
		edges: map[string][]knowledgev1.Edge{},
	}
}

// seed installs nodes into the named account graph (the local graph the resolver
// reads + writes). Used to set up the fixture before running a resolver.
func (f *k8sFake) seed(account string, nodes ...*knowledgev1.Node) {
	if f.nodes[account] == nil {
		f.nodes[account] = map[string]*knowledgev1.Node{}
	}
	for _, n := range nodes {
		f.nodes[account][n.Id] = n
	}
}

// seedEdge installs a pre-existing edge into the named account graph (used by
// edge-rewrite resolvers that read existing edges). e is taken by pointer and
// a fresh knowledgev1.Edge literal is appended: knowledgev1.Edge embeds a proto lock, so
// a by-value param + append would trip copylocks.
func (f *k8sFake) seedEdge(account string, e *knowledgev1.Edge) {
	f.edges[account] = append(f.edges[account], knowledgev1.Edge{
		FromId: e.FromId, ToId: e.ToId, Type: e.Type,
		Weight: e.Weight, Confidence: e.Confidence,
		Method: e.Method, Evidence: e.Evidence, LastValidated: e.LastValidated,
	})
}

// incomingEdges returns the set of source IDs of edges of edgeType pointing at
// target in the named account graph — the assertion read replacing
// db.IterEdges(IncomingEdges).
func (f *k8sFake) incomingEdges(account, target string, edgeType kgtypes.EdgeType) map[string]bool {
	out := map[string]bool{}
	bucket := f.edges[account]
	for i := range bucket {
		e := &bucket[i]
		if e.ToId == target && kgtypes.EdgeType(e.Type) == edgeType {
			out[e.FromId] = true
		}
	}
	return out
}

// outgoingEdges returns the edges of edgeType originating at from in the named
// account graph — the assertion read replacing db.IterEdges(OutgoingEdges).
func (f *k8sFake) outgoingEdges(account, from string, edgeType kgtypes.EdgeType) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	bucket := f.edges[account]
	for i := range bucket {
		e := &bucket[i]
		if e.FromId == from && kgtypes.EdgeType(e.Type) == edgeType {
			out = append(out, knowledgev1.Edge{
				FromId: e.FromId, ToId: e.ToId, Type: e.Type,
				Weight: e.Weight, Confidence: e.Confidence,
				Method: e.Method, Evidence: e.Evidence, LastValidated: e.LastValidated,
			})
		}
	}
	return out
}

// allEdges returns every edge in the named account graph.
func (f *k8sFake) allEdges(account string) []knowledgev1.Edge { return f.edges[account] }

// allNodes returns every node in the named account graph (order-agnostic).
func (f *k8sFake) allNodes(account string) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(f.nodes[account]))
	for _, n := range f.nodes[account] {
		out = append(out, n)
	}
	return out
}

func (f *k8sFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	acct := req.GetTarget().GetAccount()
	if m := req.GetMutation(); m != nil {
		f.ingest(acct, m)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		var infos []*knowledgev1.GraphInfo
		for a := range f.nodes {
			infos = append(infos, &knowledgev1.GraphInfo{Name: a})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.serveEdges(acct, q), nil
	default:
		return f.serveBrowse(acct, q), nil
	}
}

// ingest folds a create_batch MutationPlan into the per-account state: node
// bodies upsert by id, edges append (idx-based endpoints resolve against the
// just-added bodies, matching the store's createBatchEdges).
func (f *k8sFake) ingest(acct string, m *knowledgev1.MutationPlan) {
	switch m.GetKind() {
	case knowledgev1.MutationPlan_MUTATION_KIND_CREATE:
		if f.nodes[acct] == nil {
			f.nodes[acct] = map[string]*knowledgev1.Node{}
		}
		added := make([]string, 0, len(m.GetNodeBodies()))
		for _, b := range m.GetNodeBodies() {
			n := &knowledgev1.Node{
				Id:         b.GetId(),
				Type:       b.GetType(),
				SymbolName: b.GetName(),
				Summary:    b.GetSummary(),
				Content:    b.GetContent(),
				Metadata:   b.GetMetadata(),
			}
			f.nodes[acct][n.Id] = n
			added = append(added, n.Id)
		}
		for _, e := range m.GetEdges() {
			from := e.GetFromId()
			to := e.GetToId()
			if e.GetFromIdx() >= 0 && int(e.GetFromIdx()) < len(added) {
				from = added[e.GetFromIdx()]
			}
			if e.GetToIdx() >= 0 && int(e.GetToIdx()) < len(added) {
				to = added[e.GetToIdx()]
			}
			// Dedup by (from,to,type) to match the server's link-batch upsert-by-identity
			// semantics — a second resolver run re-emitting the same edge is a
			// no-op, so idempotency assertions hold. The fresh knowledgev1.Edge literal
			// is built at the append site so the embedded proto lock isn't copied.
			if !f.edgeExists(acct, from, to, kgtypes.EdgeType(e.GetType())) {
				f.edges[acct] = append(f.edges[acct], knowledgev1.Edge{
					FromId: from, ToId: to, Type: e.GetType(),
					Method: e.GetMethod(), Evidence: e.GetEvidence(),
				})
			}
		}
	case knowledgev1.MutationPlan_MUTATION_KIND_UNLINK:
		sel := m.GetSelection()
		spec := m.GetEdgeSpec()
		if sel == nil || spec == nil || len(sel.GetIds()) == 0 {
			return
		}
		from := sel.GetIds()[0]
		bucket := f.edges[acct]
		kept := make([]knowledgev1.Edge, 0, len(bucket))
		for i := range bucket {
			e := &bucket[i]
			if e.FromId == from && e.ToId == spec.GetToId() && e.Type == spec.GetRelationship() {
				continue // drop the unlinked edge
			}
			kept = append(kept, knowledgev1.Edge{
				FromId: e.FromId, ToId: e.ToId, Type: e.Type,
				Weight: e.Weight, Confidence: e.Confidence,
				Method: e.Method, Evidence: e.Evidence, LastValidated: e.LastValidated,
			})
		}
		f.edges[acct] = kept
	}
}

func (f *k8sFake) serveEdges(acct string, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	ids := q.GetIds()
	if len(ids) == 0 {
		return &knowledgev1.ExecuteResponse{}
	}
	forward := q.GetForward()
	want := map[string]bool{}
	for _, t := range q.GetSelection().GetEdgeTypes() {
		want[t] = true
	}
	var out []*knowledgev1.Edge
	bucket := f.edges[acct]
	for i := range bucket {
		e := &bucket[i]
		match := (forward && e.FromId == ids[0]) || (!forward && e.ToId == ids[0])
		if !match {
			continue
		}
		if len(want) > 0 && !want[e.Type] {
			continue
		}
		out = append(out, &knowledgev1.Edge{
			FromId: e.FromId, ToId: e.ToId, Type: e.Type,
			Method: e.Method, Evidence: e.Evidence,
		})
	}
	return &knowledgev1.ExecuteResponse{Edges: out}
}

func (f *k8sFake) serveBrowse(acct string, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	wantType := q.GetSelection().GetNodeType()
	wantRT := k8sMetadataEq(q.GetSelection().GetMetadataPredicates(), "resource_type")
	byID := q.GetIds()

	var matched []*knowledgev1.Node
	for _, n := range f.nodes[acct] {
		if len(byID) > 0 && !containsStr(byID, n.Id) {
			continue
		}
		if wantType != "" && n.Type != wantType {
			continue
		}
		if wantRT != "" && kgtypes.Value(n, "resource_type") != wantRT {
			continue
		}
		matched = append(matched, n)
	}
	return enginetest.ResponseWithNodes(matched...)
}

func k8sMetadataEq(preds []*knowledgev1.MetadataPredicate, key string) string {
	for _, p := range preds {
		if p.GetKey() == key && p.GetOp() == knowledgev1.MetadataPredicate_OP_EQ {
			return p.GetValue()
		}
	}
	return ""
}

func containsStr(s []string, v string) bool {
	return slices.Contains(s, v)
}

// runK8sFake is a tiny convenience so tests read clearly: seed a fake, run a
// resolver against (ctx, fake, account). Kept as a doc anchor; tests call the
// resolver directly. Marked used via a no-op reference.
var _ = func() postpopulate.GraphCaller { return newK8sFake() }

// newCtx returns a plain context for the re-homed tests (no store txn needed —
// the fake holds no real store).
func newCtx(_ testing.TB) context.Context { return context.Background() }

// edgeExists reports whether the named account graph holds an edge
// from→to of edgeType (the assertion read for resolvers that emit a single
// directed edge to a proxy).
func (f *k8sFake) edgeExists(account, from, to string, edgeType kgtypes.EdgeType) bool {
	bucket := f.edges[account]
	for i := range bucket {
		e := &bucket[i]
		if e.FromId == from && e.ToId == to && kgtypes.EdgeType(e.Type) == edgeType {
			return true
		}
	}
	return false
}

// nodeByID returns the node with id from the named account graph.
func (f *k8sFake) nodeByID(account, id string) (*knowledgev1.Node, bool) {
	n, ok := f.nodes[account][id]
	return n, ok
}
