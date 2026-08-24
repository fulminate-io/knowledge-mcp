// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// captureCaller records the last ExecuteRequest it received so a test can inspect
// the compiled MutationPlan (specifically the reflect_inert_writeback flag).
type captureCaller struct {
	last *knowledgev1.ExecuteRequest
}

func (c *captureCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.last = req
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestExecuteReflectInertMutate_SetsFlag proves the
// programmatic flag-set lands on the compiled proto, and ONLY via the inert
// helper: executeReflectInertMutate marks ReflectInertWriteback=true, while the
// plain executeViaEngine path leaves it false.
func TestExecuteReflectInertMutate_SetsFlag(t *testing.T) {
	ctx := context.Background()

	// A bulk_update_metadata call lowers to MUTATION_KIND_UPDATE_ITEMS — the same
	// shape the reflection writeback emits (cluster_id / propagated_*).
	bulkArgs, err := json.Marshal(map[string]any{
		"operation": "bulk_update_metadata",
		"updates": []map[string]any{
			{"id": "th-1", "metadata": map[string]string{"cluster_id": "c-1"}},
		},
	})
	require.NoError(t, err)

	// (1) Inert helper sets the flag on the compiled MutationPlan.
	inert := &captureCaller{}
	err = executeReflectInertMutate(ctx, inert, bulkArgs)
	require.NoError(t, err)
	require.NotNil(t, inert.last)
	require.NotNil(t, inert.last.GetMutation(), "bulk_update_metadata must compile to a MutationPlan")
	assert.True(t, inert.last.GetMutation().GetReflectInertWriteback(),
		"executeReflectInertMutate must set ReflectInertWriteback=true")

	// (2) Plain executeViaEngine leaves the flag false.
	plain := &captureCaller{}
	_, err = executeViaEngine(ctx, plain, "mutate", bulkArgs)
	require.NoError(t, err)
	require.NotNil(t, plain.last)
	require.NotNil(t, plain.last.GetMutation())
	assert.False(t, plain.last.GetMutation().GetReflectInertWriteback(),
		"executeViaEngine must NOT set ReflectInertWriteback")
}

// passRecorder is a Caller + reflectProbe that records every ExecuteRequest the
// WHOLE reflection pass issues, and serves the minimum corpus that lets the pass
// reach all three of its write classes.
//
// THE BRACKET IS runPass, NOT runClusterDetection, and that is the point of the
// test below. Only ONE of the three classes happens inside detection: cluster
// assignments. The propagated_valence / propagated_magnitude writeback happens in
// RunPropagationScoped AFTER detection returns, and both watermark upserts happen
// later still. A recorder scoped to detection would go green while covering one
// class of three — including the thought-typed write that most needs the flag.
type passRecorder struct {
	mu       sync.Mutex
	requests []*knowledgev1.ExecuteRequest

	thoughts []*knowledgev1.Node
	probeGen uint64
	nodes    map[string]*knowledgev1.Node // tiny by-id store so watermarks round-trip
}

// PipelineScan makes this fake satisfy the package-local reflectProbe. A NON-ZERO
// DirtyGen is REQUIRED: runPass writes the gen watermark only when the probe
// succeeded AND the gen is non-zero, so an Execute-only fake would silently cover
// two write classes instead of three.
func (r *passRecorder) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{DirtyGen: r.probeGen}, nil
}

func (r *passRecorder) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()

	if m := req.GetMutation(); m != nil {
		for _, b := range m.GetNodeBodies() {
			n := &knowledgev1.Node{Id: b.GetId(), Type: b.GetType(), SymbolName: b.GetName()}
			for k, v := range b.GetMetadata() {
				kgtypes.SetValue(n, k, v)
			}
			r.mu.Lock()
			r.nodes[b.GetId()] = n
			r.mu.Unlock()
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if id := q.GetById(); id != "" {
		r.mu.Lock()
		n, ok := r.nodes[id]
		r.mu.Unlock()
		if ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(r.edges(), q)}, nil
	}
	if len(q.GetIds()) > 0 {
		return &knowledgev1.ExecuteResponse{Nodes: r.byIDs(q.GetIds())}, nil
	}
	// A type browse. The pass drains with an id-keyset cursor, so serve only ids
	// strictly after it; every non-thought type is empty.
	sel := q.GetSelection()
	if sel == nil || sel.GetNodeType() != string(kgtypes.NodeThought) {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var page []*knowledgev1.Node
	for _, n := range r.thoughts {
		if n.GetId() <= q.GetAfterId() {
			continue
		}
		page = append(page, n)
	}
	return &knowledgev1.ExecuteResponse{Nodes: page}, nil
}

// edges gives every thought a charge and chains the thoughts together, so the pass
// has real charge data to propagate — a corpus with no charges produces no
// propagated_* writeback and the test would cover one class fewer.
func (r *passRecorder) edges() []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for i, n := range r.thoughts {
		out = append(out, &knowledgev1.Edge{
			Type: string(kgtypes.EdgeChargedBy), FromId: n.GetId(), ToId: "c-" + n.GetId(),
		})
		if i > 0 {
			out = append(out, &knowledgev1.Edge{
				Type: string(kgtypes.EdgeRelatesTo), FromId: r.thoughts[i-1].GetId(), ToId: n.GetId(),
			})
		}
	}
	return out
}

func (r *passRecorder) byIDs(ids []string) []*knowledgev1.Node {
	byID := map[string]*knowledgev1.Node{}
	for _, n := range r.thoughts {
		byID[n.GetId()] = n
		charge := &knowledgev1.Node{Id: "c-" + n.GetId(), Type: string(kgtypes.NodeCharge), UpdatedAt: n.GetUpdatedAt()}
		kgtypes.SetValue(charge, "polarity", "positive")
		kgtypes.SetValue(charge, "weight", "5")
		byID[charge.GetId()] = charge
	}
	var out []*knowledgev1.Node
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			out = append(out, n)
		}
	}
	return out
}

func (r *passRecorder) mutations() []*knowledgev1.MutationPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*knowledgev1.MutationPlan
	for _, req := range r.requests {
		if m := req.GetMutation(); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// TestReflectionPass_AllWritesReflectInert brackets the WHOLE pass and requires
// every mutation it issues to be reflect-inert, or to target a node type outside
// the reflection corpus {thought, charge}. Otherwise the pass advances the reflect
// dirty-gen with its own writeback and re-triggers itself forever — and with N
// clients sharing one graph, each client's writeback wakes all the others.
func TestReflectionPass_AllWritesReflectInert(t *testing.T) {
	thoughts := make([]*knowledgev1.Node, 0, 4)
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		// Non-zero UpdatedAt is REQUIRED: the UpdatedAt watermark is written only
		// when the tick watermark is non-zero, so a zero-stamped corpus would skip
		// that write and quietly cover one class fewer.
		thoughts = append(thoughts, &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1_700_000_000,
		})
	}
	rec := &passRecorder{thoughts: thoughts, probeGen: 42, nodes: map[string]*knowledgev1.Node{}}

	// The persisted gen must DIFFER from the probe gen, or the quiet-tick gate skips
	// the entire pass and the recorder observes zero writes.
	require.NoError(t, writeLastReflectedGen(context.Background(), rec, 7))
	setupWrites := len(rec.mutations())
	require.Equal(t, 1, setupWrites, "setup wrote exactly one watermark")

	loop := &PropagationLoop{gc: rec, stopCh: make(chan struct{})}
	_, err := loop.runPass(context.Background(), false)
	require.NoError(t, err)

	muts := rec.mutations()[setupWrites:] // drop the setup write
	require.NotEmpty(t, muts, "the pass must issue mutations — a pass that wrote nothing passes vacuously")

	// THE GATE.
	for i, mp := range muts {
		if mp.GetReflectInertWriteback() {
			continue
		}
		for _, b := range mp.GetNodeBodies() {
			assert.NotContains(t,
				[]string{string(kgtypes.NodeThought), string(kgtypes.NodeCharge)}, b.GetType(),
				"mutation %d writes reflection-corpus node %s (type %s) WITHOUT reflect_inert_writeback — "+
					"this pass would advance the reflect dirty-gen with its own writeback and re-trigger itself",
				i, b.GetId(), b.GetType())
		}
	}

	// NON-VACUITY: each of the three write classes must actually be present, or the
	// gate above is asserting over a set that happens to be missing the risky one.
	var sawInertWriteback, sawGenWatermark, sawUpdatedAtWatermark bool
	for _, mp := range muts {
		if mp.GetReflectInertWriteback() {
			sawInertWriteback = true
		}
		for _, b := range mp.GetNodeBodies() {
			switch b.GetId() {
			case watermarkNodeID:
				if _, ok := b.GetMetadata()[watermarkGenKey]; ok {
					sawGenWatermark = true
				}
			case reflectWatermarkNodeID:
				if _, ok := b.GetMetadata()[reflectWatermarkKey]; ok {
					sawUpdatedAtWatermark = true
				}
			}
		}
	}
	assert.True(t, sawInertWriteback,
		"the pass's OWN metadata writeback (cluster assignments / propagated_*) must be present AND flagged inert")
	assert.True(t, sawGenWatermark,
		"the reflect-gen watermark upsert must be present — a zero probe gen would silently skip it")
	assert.True(t, sawUpdatedAtWatermark,
		"the UpdatedAt watermark upsert must be present — a zero-stamped corpus would silently skip it")
}
