// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// diffWritebackFake captures every bulk_update_metadata payload's member IDs (per
// metadata key) so a test can assert the writeback carried ONLY the nodes whose
// value changed. It serves the same corpus reads equivalenceFake does but does NOT
// apply writes back (the test inspects the payload, not post-state).
type diffWritebackFake struct {
	mu sync.Mutex

	thoughts map[string]*knowledgev1.Node
	order    []string
	edges    [][2]string

	// captured payload IDs by metadata key, from the most recent writeback.
	lastWriteIDsByKey map[string][]string
}

func newDiffWritebackFake(ids []string, edges [][2]string) *diffWritebackFake {
	f := &diffWritebackFake{
		thoughts: make(map[string]*knowledgev1.Node, len(ids)),
		order:    append([]string(nil), ids...),
		edges:    edges,
	}
	for _, id := range ids {
		f.thoughts[id] = &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	}
	return f
}

func (f *diffWritebackFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m := req.GetMutation(); m != nil {
		byKey := map[string][]string{}
		for _, it := range m.GetUpdateItems() {
			for k := range it.GetMetadata() {
				byKey[k] = append(byKey[k], it.GetId())
			}
		}
		f.lastWriteIDsByKey = byKey
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// UNION, not first-match: the wire returns every edge matching ANY requested
		// type (an empty set means every type). This fixture holds only relates-to
		// edges, so it serves them whenever relates-to is requested and contributes
		// nothing for kg-contains / charged-by. Bailing out as soon as the request
		// MENTIONED either of those was adequate only while each caller asked for a
		// single type; the unified pivot read asks for seven at once.
		want := map[string]bool{}
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				want[et] = true
			}
		}
		if len(want) > 0 && !want[string(kgtypes.EdgeRelatesTo)] {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		var out []*knowledgev1.Edge
		for _, e := range f.edges {
			out = append(out, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: e[0], ToId: e[1]})
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(out, q)}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
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

func (f *diffWritebackFake) nodeByIDFrom() map[string]*knowledgev1.Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*knowledgev1.Node, len(f.thoughts))
	for id, n := range f.thoughts {
		out[id] = cloneNode(n)
	}
	return out
}

func (f *diffWritebackFake) capturedIDs(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := append([]string(nil), f.lastWriteIDsByKey[key]...)
	sort.Strings(ids)
	return ids
}

// TestDiffWriteback_PropagatedOnlyChangedNodes (FAILS-WHEN-ABSENT) proves the
// propagated_* writeback carries ONLY nodes whose value changed. A reference full
// pass establishes the converged propagated_* for every node; those values are
// persisted for the WHOLE corpus EXCEPT one node left deliberately stale. A second
// scoped pass over the same (unchanged) graph then recomputes the same converged
// values — so diffMetadataUpdates must write back ONLY the one stale node, not the
// whole closure. Fails if the diff gate is bypassed (every member re-written).
func TestDiffWriteback_PropagatedOnlyChangedNodes(t *testing.T) {
	ctx := context.Background()

	ids := []string{"a1", "a2", "a3"}
	edges := [][2]string{{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"}}

	// Reference full pass (applying fake) to learn the converged propagated_* per
	// node by reading post-write state.
	refState := newDiffWritebackFakeApplying(ids, edges)
	_, err := RunPropagationScoped(ctx, refState, nil, refState.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)
	converged := map[string][2]string{}
	for id, n := range refState.thoughts {
		converged[id] = [2]string{kgtypes.Value(n, "propagated_valence"), kgtypes.Value(n, "propagated_magnitude")}
	}

	// Persist the converged values for the WHOLE corpus EXCEPT a3 (left stale).
	scoped := newDiffWritebackFake(ids, edges)
	scoped.mu.Lock()
	for id, pv := range converged {
		if id == "a3" {
			continue // leave a3 with no persisted propagated_* → it WILL change.
		}
		kgtypes.SetValue(scoped.thoughts[id], "propagated_valence", pv[0])
		kgtypes.SetValue(scoped.thoughts[id], "propagated_magnitude", pv[1])
	}
	scoped.mu.Unlock()

	// Scoped pass touching the (single) component; recompute equals persisted for
	// a1/a2 → dropped; a3 differs (was unset) → the ONLY row written.
	seed := map[string]bool{"a1": true}
	_, err = RunPropagationScoped(ctx, scoped, nil, scoped.nodeByIDFrom(), seed, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"a3"}, scoped.capturedIDs("propagated_valence"),
		"only the node whose propagated_* changed (a3) is written — not the whole closure")
}

// TestDeadband_ConvergedCorpusZeroWriteback (FAILS-WHEN-ABSENT for the persistence
// deadband) proves that a full pass over a corpus whose persisted propagated_*
// values are already within the deadband of the recomputed values writes ZERO
// rows. A reference full pass learns the converged propagated_* per node; those
// values PERTURBED by +5e-5 (50x the %.6f quantum, so the exact-string comparison
// always sees a difference, yet 5e-5 sits below the 1e-4 deadband) are persisted
// for the whole corpus. A second full pass recomputes the same converged values —
// every row is within the deadband, so the writeback carries NOTHING. Under
// exact-string diffing (no deadband) every row's string differs → the whole corpus
// is re-written and this test fails.
func TestDeadband_ConvergedCorpusZeroWriteback(t *testing.T) {
	ctx := context.Background()

	ids := []string{"a1", "a2", "a3"}
	edges := [][2]string{{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"}}

	// Reference full pass (applying fake) to learn the converged propagated_* per node.
	refState := newDiffWritebackFakeApplying(ids, edges)
	_, err := RunPropagationScoped(ctx, refState, nil, refState.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)

	// Persist the converged values PERTURBED by +5e-5 for the WHOLE corpus. 5e-5 is
	// 50x the %.6f quantum (exact-string diff always sees a change) but below the
	// 1e-4 deadband (the deadband gate must drop every row).
	scoped := newDiffWritebackFake(ids, edges)
	scoped.mu.Lock()
	for id, n := range refState.thoughts {
		for _, key := range []string{"propagated_valence", "propagated_magnitude"} {
			perturbed := parseFloat(kgtypes.Value(n, key)) + 5e-5
			kgtypes.SetValue(scoped.thoughts[id], key, fmt.Sprintf("%.6f", perturbed))
		}
	}
	scoped.mu.Unlock()

	// Full pass (nil seed) over the same graph: recompute equals persisted within the
	// deadband for every node → zero rows written.
	_, err = RunPropagationScoped(ctx, scoped, nil, scoped.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)

	assert.Empty(t, scoped.capturedIDs("propagated_valence"),
		"no propagated_valence row is written when every recomputed value is within the deadband of persisted")
	assert.Empty(t, scoped.capturedIDs("propagated_magnitude"),
		"no propagated_magnitude row is written when every recomputed value is within the deadband of persisted")
}

// TestDiffWriteback_ClusterIDOnlyChangedNodes (FAILS-WHEN-ABSENT) proves the
// cluster_id writeback carries only members whose cluster_id changed. With the
// canonical min-member label scheme, an unchanged partition yields identical
// cluster_id, so a second detection over an already-persisted partition writes
// ZERO rows (and a member with a stale cluster_id writes exactly one).
func TestDiffWriteback_ClusterIDOnlyChangedNodes(t *testing.T) {
	ctx := context.Background()
	groups := map[string][]string{"a1": {"a1", "a2", "a3"}}

	f := newDiffWritebackFake([]string{"a1", "a2", "a3"}, nil)
	// Persist the canonical cluster_id ("a1") for a1,a2 — leave a3 stale.
	f.mu.Lock()
	kgtypes.SetValue(f.thoughts["a1"], "cluster_id", "a1")
	kgtypes.SetValue(f.thoughts["a2"], "cluster_id", "a1")
	kgtypes.SetValue(f.thoughts["a3"], "cluster_id", "STALE")
	f.mu.Unlock()

	buildClusterObjects(ctx, f, groups, nil)

	assert.Equal(t, []string{"a3"}, f.capturedIDs("cluster_id"),
		"only the member whose cluster_id changed (a3: STALE→a1) is written; a1/a2 unchanged are dropped")
}

// newDiffWritebackFakeApplying is a variant that APPLIES the writeback back into
// node state (used only to read the converged propagated_* reference values).
func newDiffWritebackFakeApplying(ids []string, edges [][2]string) *applyingDiffFake {
	return &applyingDiffFake{diffWritebackFake: newDiffWritebackFake(ids, edges)}
}

type applyingDiffFake struct{ *diffWritebackFake }

func (f *applyingDiffFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.mu.Lock()
		for _, it := range m.GetUpdateItems() {
			if n := f.thoughts[it.GetId()]; n != nil {
				for k, v := range it.GetMetadata() {
					kgtypes.SetValue(n, k, v)
				}
			}
		}
		f.mu.Unlock()
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return f.diffWritebackFake.Execute(ctx, req)
}
