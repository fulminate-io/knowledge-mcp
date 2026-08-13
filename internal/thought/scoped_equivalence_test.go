// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// equivalenceFake is a stateful Caller for the capstone equivalence tests. It
// models a thought corpus (nodes with UpdatedAt + cluster_id + propagated_*), the
// adjacency edges (over the 5 thought-cluster edge types), and APPLIES the loop's
// metadata writeback (cluster_id / propagated_*) back into node state so a second
// pass reads what the first persisted. It captures the propagated_* writeback rows
// of the most recent pass for the per-component invariance comparison.
type equivalenceFake struct {
	mu sync.Mutex

	thoughts map[string]*knowledgev1.Node
	order    []string
	// undirected adjacency edges, each as a {from,to} pair over a thought-cluster
	// edge type (EdgeRelatesTo here — any of adjacencyEdgeTypes works).
	edges [][2]string

	// lastPropagated captures id → {propagated_valence, propagated_magnitude} from
	// the most recent bulk_update_metadata writeback that carried propagated_*.
	lastPropagated map[string]map[string]string
}

func newEquivalenceFake(ids []string, edges [][2]string) *equivalenceFake {
	f := &equivalenceFake{
		thoughts: make(map[string]*knowledgev1.Node, len(ids)),
		order:    append([]string(nil), ids...),
		edges:    edges,
	}
	for _, id := range ids {
		f.thoughts[id] = &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	}
	return f
}

func (f *equivalenceFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m := req.GetMutation(); m != nil {
		// Apply propagated_* / cluster_id writebacks back into node state and
		// capture the propagated_* rows.
		captured := map[string]map[string]string{}
		for _, it := range m.GetUpdateItems() {
			n := f.thoughts[it.GetId()]
			meta := it.GetMetadata()
			if n != nil {
				for k, v := range meta {
					kgtypes.SetValue(n, k, v)
				}
			}
			if _, hasV := meta["propagated_valence"]; hasV {
				captured[it.GetId()] = map[string]string{
					"propagated_valence":   meta["propagated_valence"],
					"propagated_magnitude": meta["propagated_magnitude"],
				}
			}
		}
		if len(captured) > 0 {
			f.lastPropagated = captured
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Adjacency-edge read (thought-cluster edge types) returns our corpus edges;
		// the EdgeKGContains session read + charged-by read return nothing.
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				if et == string(kgtypes.EdgeKGContains) || et == string(kgtypes.EdgeChargedBy) {
					return &knowledgev1.ExecuteResponse{}, nil
				}
			}
		}
		var out []*knowledgev1.Edge
		for _, e := range f.edges {
			out = append(out, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: e[0], ToId: e[1]})
		}
		return &knowledgev1.ExecuteResponse{Edges: out}, nil
	}

	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil // no singletons needed here.
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range f.order {
		nodes = append(nodes, cloneNode(f.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// nodeByIDFrom hydrates the fake's current node state into the nodeByID map
// RunPropagationScoped consumes for carry-forward/diff.
func (f *equivalenceFake) nodeByIDFrom() map[string]*knowledgev1.Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*knowledgev1.Node, len(f.thoughts))
	for id, n := range f.thoughts {
		out[id] = cloneNode(n)
	}
	return out
}

// propagatedSnapshot reads every node's persisted propagated_* into a comparable
// map, AFTER the fake has applied a pass's writeback.
func (f *equivalenceFake) propagatedSnapshot() map[string][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][2]string, len(f.thoughts))
	for id, n := range f.thoughts {
		out[id] = [2]string{kgtypes.Value(n, "propagated_valence"), kgtypes.Value(n, "propagated_magnitude")}
	}
	return out
}

// TestScopedEqualsFull_PerComponentInvariance (CASE A, FAILS-WHEN-ABSENT) proves a
// scoped pass with a single-component dirty seed produces propagated_* IDENTICAL
// to a full pass for EVERY node: the touched component recomputes to the same
// values, untouched components carry forward exactly.
func TestScopedEqualsFull_PerComponentInvariance(t *testing.T) {
	ctx := context.Background()

	// Two disjoint components: A={a1,a2,a3} triangle, B={b1,b2,b3} triangle.
	ids := []string{"a1", "a2", "a3", "b1", "b2", "b3"}
	edges := [][2]string{
		{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"},
		{"b1", "b2"}, {"b2", "b3"}, {"b1", "b3"},
	}

	// FULL reference pass (nil seed): records propagated_* for every node, applied
	// back into the fake's state.
	full := newEquivalenceFake(ids, edges)
	_, err := RunPropagationScoped(ctx, full, nil, full.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)
	reference := full.propagatedSnapshot()
	require.Len(t, reference, len(ids))

	// SCOPED pass: same corpus, but seed touches ONLY component A. Pre-seed the
	// fake with the SAME persisted propagated_* the full pass produced (so the
	// carry-forward for B has something to carry), then run scoped.
	scoped := newEquivalenceFake(ids, edges)
	scoped.mu.Lock()
	for id, pv := range reference {
		kgtypes.SetValue(scoped.thoughts[id], "propagated_valence", pv[0])
		kgtypes.SetValue(scoped.thoughts[id], "propagated_magnitude", pv[1])
	}
	scoped.mu.Unlock()

	seed := map[string]bool{"a1": true} // touches component A only.
	_, err = RunPropagationScoped(ctx, scoped, nil, scoped.nodeByIDFrom(), seed, nil)
	require.NoError(t, err)
	got := scoped.propagatedSnapshot()

	// BYTE-IDENTICAL for ALL nodes: component A recomputed to the same values,
	// component B carried forward (diff dropped its unchanged rows → state stands).
	assert.Equal(t, reference, got,
		"scoped pass propagated_* must be byte-identical to the full pass for every node")
}

// TestScopedEqualsFull_ClusterIDStable (CASE A, cluster_id leg) proves the
// canonical cluster_id is identical across a full and a scoped detection over the
// same partition — the regression guard for the stable-label + groupKey fixes.
func TestScopedEqualsFull_ClusterIDStable(t *testing.T) {
	ctx := context.Background()
	// Same two-component partition expressed as groups (canonical min-member keys).
	groups := map[string][]string{
		"a1": {"a1", "a2", "a3"},
		"b1": {"b1", "b2", "b3"},
	}
	run := func() map[string]string {
		f := newEquivalenceFake([]string{"a1", "a2", "a3", "b1", "b2", "b3"}, nil)
		clusters := buildClusterObjects(ctx, f, groups, nil)
		require.Len(t, clusters, 2)
		out := map[string]string{}
		f.mu.Lock()
		for id, n := range f.thoughts {
			out[id] = kgtypes.Value(n, "cluster_id")
		}
		f.mu.Unlock()
		return out
	}
	first, second := run(), run()
	assert.Equal(t, first, second,
		"cluster_id assignment must be identical across detection runs over the same partition")
}

// TestScopedEqualsFull_BridgingJoin (CASE B, FAILS-WHEN-ABSENT) proves a bridging
// edge fed as the seed pulls BOTH components into the closure, and the scoped
// propagation over the bridged corpus equals a full pass over the same bridged
// corpus. Without closure expansion over the NEW adjacency the join is missed.
func TestScopedEqualsFull_BridgingJoin(t *testing.T) {
	ctx := context.Background()

	// Two dense triangles bridged densely so the components fuse into ONE.
	ids := []string{"a1", "a2", "a3", "b1", "b2", "b3"}
	bridged := [][2]string{
		{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"},
		{"b1", "b2"}, {"b2", "b3"}, {"b1", "b3"},
		// dense bridge crossing the two triangles.
		{"a1", "b1"}, {"a2", "b2"}, {"a3", "b3"}, {"a1", "b2"}, {"a3", "b1"},
	}

	// FULL pass over the bridged corpus (reference).
	full := newEquivalenceFake(ids, bridged)
	_, err := RunPropagationScoped(ctx, full, nil, full.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)
	reference := full.propagatedSnapshot()

	// SCOPED pass: the bridging edges are the delta, so BOTH endpoints seed the
	// closure. findConnectedComponents over the bridged adjacency yields ONE
	// component containing both endpoints, so the closure spans the whole corpus.
	scoped := newEquivalenceFake(ids, bridged)
	seed := map[string]bool{"a1": true, "b1": true} // bridge endpoints.
	_, err = RunPropagationScoped(ctx, scoped, nil, scoped.nodeByIDFrom(), seed, nil)
	require.NoError(t, err)
	got := scoped.propagatedSnapshot()

	assert.Equal(t, reference, got,
		"scoped pass over the bridged corpus must equal a full pass — the bridging-edge JOIN pulls both components into the closure")

	// Sanity: the closure spanned every node (single fused component recomputed).
	var recomputed []string
	for id, pv := range got {
		if pv[0] != "" {
			recomputed = append(recomputed, id)
		}
	}
	sort.Strings(recomputed)
	assert.Equal(t, ids, recomputed, "every node in the fused component was recomputed")
}
