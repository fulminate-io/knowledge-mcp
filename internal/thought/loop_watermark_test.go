// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// watermarkLoopFake is a tiny stateful store for the watermark self-trigger guard.
// It models the thought corpus (nodes with cluster_id + UpdatedAt), one session's
// kg_contains membership, the singleton watermark nodes, and APPLIES the loop's
// own cluster_id + watermark writes back into its state so a SECOND tick reads
// what the FIRST tick persisted. cluster_id bulk_update_metadata rows are recorded
// per tick so the test can assert the quiet tick writes zero.
type watermarkLoopFake struct {
	mu sync.Mutex

	// thought id → node (carries cluster_id metadata + UpdatedAt).
	thoughts map[string]*knowledgev1.Node
	order    []string // stable browse order.
	// session id → member thought ids (kg_contains).
	session     string
	sessionMems []string
	// singleton resource nodes (watermark + gen), by id.
	singletons map[string]*knowledgev1.Node

	// per-tick capture of cluster_id writeback member rows.
	clusterWriteRows []int
}

func newWatermarkLoopFake() *watermarkLoopFake {
	// Two thoughts in one session, stable UpdatedAt (no external change between
	// ticks). They co-cluster (the session sibling edge connects them).
	f := &watermarkLoopFake{
		thoughts: map[string]*knowledgev1.Node{
			"t1": {Id: "t1", Type: string(kgtypes.NodeThought), UpdatedAt: 1000},
			"t2": {Id: "t2", Type: string(kgtypes.NodeThought), UpdatedAt: 1000},
		},
		order:       []string{"t1", "t2"},
		session:     "s1",
		sessionMems: []string{"t1", "t2"},
		singletons:  map[string]*knowledgev1.Node{},
	}
	return f
}

func (f *watermarkLoopFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Mutations: upsert (watermark singletons) + bulk_update_metadata (cluster_id).
	if m := req.GetMutation(); m != nil {
		// Singleton upserts (watermark / gen) arrive as NodeBodies.
		for _, b := range m.GetNodeBodies() {
			n := &knowledgev1.Node{Id: b.GetId(), Type: b.GetType(), SymbolName: b.GetName()}
			for k, v := range b.GetMetadata() {
				kgtypes.SetValue(n, k, v)
			}
			f.singletons[b.GetId()] = n
		}
		// cluster_id bulk_update_metadata arrives as UpdateItems — apply back into
		// the thought state so the NEXT tick reads the persisted cluster_id.
		rows := 0
		for _, it := range m.GetUpdateItems() {
			if cid, ok := it.GetMetadata()["cluster_id"]; ok {
				if n := f.thoughts[it.GetId()]; n != nil {
					kgtypes.SetValue(n, "cluster_id", cid)
				}
				rows++
			}
		}
		if rows > 0 {
			f.clusterWriteRows = append(f.clusterWriteRows, rows)
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// by-id singleton read (watermark).
	if q.GetById() != "" {
		if n, ok := f.singletons[q.GetById()]; ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// edges reads: adjacency-edge set (no kg_contains in our corpus) + kg_contains
	// session membership.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Only the EdgeKGContains-filtered read returns our session edges; the
		// adjacency-edge read (Next/RelatesTo/...) and charges read return nothing.
		if sel := q.GetSelection(); sel != nil && slices.Contains(sel.GetEdgeTypes(), string(kgtypes.EdgeKGContains)) {
			var edges []*knowledgev1.Edge
			for _, m := range f.sessionMems {
				edges = append(edges, &knowledgev1.Edge{
					Type: string(kgtypes.EdgeKGContains), FromId: f.session, ToId: m,
				})
			}
			return &knowledgev1.ExecuteResponse{Edges: edges}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// ids[] hydrate (cluster member nodes / nodeByID).
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}

	// type=thought paged browse: serve the corpus at offset 0, empty afterwards.
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range f.order {
		nodes = append(nodes, cloneNode(f.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// cloneNode returns a shallow copy with a copied metadata map so a reader cannot
// mutate the fake's stored state.
func cloneNode(n *knowledgev1.Node) *knowledgev1.Node {
	cp := &knowledgev1.Node{Id: n.Id, Type: n.Type, SymbolName: n.SymbolName, UpdatedAt: n.UpdatedAt}
	for k, v := range n.Metadata {
		kgtypes.SetValue(cp, k, v)
	}
	return cp
}

// TestWatermark_SelfTriggerGuard_QuietTickAfterRelabel (FAILS-WHEN-ABSENT) proves
// the loop's own writeback does NOT re-seed the next tick. Two consecutive warm
// ticks with NO external change: the FIRST tick may relabel (one-time, cold-start
// full pass writes cluster_id) and persists the max-UpdatedAt watermark; the
// SECOND (quiet) tick must produce an EMPTY dirty seed and ZERO cluster_id
// writeback rows — because the watermark covers every node's UpdatedAt and
// diffMetadataUpdates drops the unchanged cluster_id rows.
func TestWatermark_SelfTriggerGuard_QuietTickAfterRelabel(t *testing.T) {
	fake := newWatermarkLoopFake()
	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}

	// TICK 1 (cold start → full pass): runBackgroundPropagation runs detection +
	// scoped propagation, assigns cluster_id, and persists the max-UpdatedAt
	// watermark on completion.
	p.runBackgroundPropagation()

	// The watermark singleton must now be persisted at the corpus max UpdatedAt.
	require.Contains(t, fake.singletons, reflectWatermarkNodeID,
		"tick 1 must persist the max-UpdatedAt watermark singleton")
	assert.Equal(t, "1000", kgtypes.Value(fake.singletons[reflectWatermarkNodeID], reflectWatermarkKey),
		"watermark equals max(Node.UpdatedAt) over the full browse")

	// TICK 2 (quiet — no external change): empty seed, zero cluster_id writeback.
	tick1WriteCount := len(fake.clusterWriteRows)
	p.runBackgroundPropagation()

	p.mu.Lock()
	seed := p.lastDirtySeed
	p.mu.Unlock()
	assert.Empty(t, seed,
		"the quiet tick's dirty seed must be empty (no UpdatedAt>watermark, no edge change)")

	// No NEW cluster_id writeback row landed on tick 2 (diff dropped them all).
	assert.Len(t, fake.clusterWriteRows, tick1WriteCount,
		"the quiet tick must emit ZERO cluster_id writeback rows (diff drops unchanged labels)")
}
