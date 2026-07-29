// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// propagationCorpus serves every read RunPropagationScoped issues: the type=thought
// browse (fetchAllThoughtNodes), the adjacency edge read (relates-to), the
// EdgeKGContains read (no sessions → empty), the EdgeChargedBy read + charge
// hydrate (fetchChargesFor / chargeMapForThoughts), node hydrate, and the bulk
// writeback mutation (accepted, no-op). It models a graph of disjoint components.
type propagationCorpus struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node
	chargeOf map[string][]string
	adjEdges []*knowledgev1.Edge

	// captured writeback payload member IDs by metadata key, from the most recent
	// bulk_update_metadata mutation. diffWritebackFake cannot build this fixture (it
	// empties EdgeChargedBy), so the capture lives here alongside the charge/edge seam.
	lastWriteIDsByKey map[string][]string
}

func newPropagationCorpus() *propagationCorpus {
	return &propagationCorpus{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string][]string{},
	}
}

func (c *propagationCorpus) addThought(id, polarity string) {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
	if polarity == "" {
		return
	}
	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", "7")
	c.charges[chID] = ch
	c.chargeOf[id] = []string{chID}
}

func (c *propagationCorpus) addEdge(from, to string) {
	c.adjEdges = append(c.adjEdges, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: from, ToId: to})
}

func (c *propagationCorpus) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		byKey := map[string][]string{}
		for _, it := range m.GetUpdateItems() {
			for k := range it.GetMetadata() {
				byKey[k] = append(byKey[k], it.GetId())
			}
		}
		c.lastWriteIDsByKey = byKey
		return &knowledgev1.ExecuteResponse{}, nil // accept the writeback, capture its IDs.
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		var wantCharged, wantContains bool
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				switch et {
				case string(kgtypes.EdgeChargedBy):
					wantCharged = true
				case string(kgtypes.EdgeKGContains):
					wantContains = true
				}
			}
		}
		if wantCharged {
			var ce []*knowledgev1.Edge
			for _, tid := range c.order {
				for _, chID := range c.chargeOf[tid] {
					ce = append(ce, &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: tid, ToId: chID})
				}
			}
			return &knowledgev1.ExecuteResponse{Edges: ce}, nil
		}
		if wantContains {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: c.adjEdges}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			} else if ch, ok := c.charges[id]; ok {
				nodes = append(nodes, cloneNode(ch))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range c.order {
		nodes = append(nodes, cloneNode(c.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// capturedIDs returns the sorted member IDs written for one metadata key by the
// most recent writeback mutation (empty when nothing was written for that key).
func (c *propagationCorpus) capturedIDs(key string) []string {
	ids := append([]string(nil), c.lastWriteIDsByKey[key]...)
	sort.Strings(ids)
	return ids
}

// nodeByIDFrom snapshots the corpus nodes into the nodeByID map RunPropagationScoped
// reads persisted propagated_* from (via currentPropagatedAccessor).
func (c *propagationCorpus) nodeByIDFrom() map[string]*knowledgev1.Node {
	out := make(map[string]*knowledgev1.Node, len(c.thoughts))
	for id, n := range c.thoughts {
		out[id] = cloneNode(n)
	}
	return out
}

// applyingPropagationCorpus APPLIES the writeback back into node state (used only to
// read the converged/capped propagated_* reference values), mirroring the corpus's
// read seam for everything else.
type applyingPropagationCorpus struct{ *propagationCorpus }

func (c *applyingPropagationCorpus) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		for _, it := range m.GetUpdateItems() {
			if n := c.thoughts[it.GetId()]; n != nil {
				for k, v := range it.GetMetadata() {
					kgtypes.SetValue(n, k, v)
				}
			}
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return c.propagationCorpus.Execute(ctx, req)
}

// TestRunPropagation_PerComponentConvergence (FAILS-WHEN-ABSENT) proves per-component
// convergence reporting: among M components, a single long charge-driven path that
// does NOT converge within the iteration cap yields ComponentsConverged=M-1 and
// exactly one NonConverged entry carrying that component's size + a non-zero
// residual, while the converged components are counted, not masked. Goes red if the
// global converged-AND flag returns or the per-component detail is dropped.
func TestRunPropagation_PerComponentConvergence(t *testing.T) {
	c := newPropagationCorpus()

	// A long path component (p0..pN) with opposite-charge endpoints and uncharged
	// interior: valence diffuses one hop per iteration, so a path longer than the
	// iteration cap (defaultMaxIterations=100) cannot converge in time.
	pathLen := 160
	for i := range pathLen {
		pol := ""
		switch i {
		case 0:
			pol = "positive"
		case pathLen - 1:
			pol = "negative"
		}
		c.addThought(fmt.Sprintf("p%d", i), pol)
	}
	for i := 0; i < pathLen-1; i++ {
		c.addEdge(fmt.Sprintf("p%d", i), fmt.Sprintf("p%d", i+1))
	}

	// Several tiny converging components: isolated charged singletons (no edges →
	// each its own component, trivially converged at iteration 1).
	converged := 4
	for i := range converged {
		c.addThought(fmt.Sprintf("iso%d", i), "positive")
	}

	res, err := RunPropagation(context.Background(), c, nil, nil, nil)
	require.NoError(t, err)

	require.Len(t, res.NonConverged, 1, "exactly one component (the long path) fails to converge")
	assert.Equal(t, pathLen, res.NonConverged[0].Size, "the non-converged entry carries the path component's size")
	assert.Greater(t, res.NonConverged[0].ValenceResidual, 0.0, "the non-converged entry carries a non-zero residual")

	assert.Equal(t, res.Components-1, res.ComponentsConverged,
		"every component except the long path converged (M-1)")
	assert.False(t, res.Converged, "Converged is false while any component is non-converged")
}

// TestRunPropagation_AllConverged (FAILS-WHEN-ABSENT) proves Converged derives true
// (len(NonConverged)==0) when every component converges.
func TestRunPropagation_AllConverged(t *testing.T) {
	c := newPropagationCorpus()
	for i := range 5 {
		c.addThought(fmt.Sprintf("iso%d", i), "positive") // isolated → trivially converges.
	}
	res, err := RunPropagation(context.Background(), c, nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, res.NonConverged)
	assert.Equal(t, res.Components, res.ComponentsConverged)
	assert.True(t, res.Converged, "all components converged ⇒ Converged derives true")
}

// buildNonConvergedPath builds the 160-node charge-gradient long path: opposite-charge
// endpoints, uncharged interior, one edge per hop. Valence diffuses one hop per
// iteration, so a path longer than the iteration cap cannot converge and its valence
// oscillates supra-epsilon between ticks. Magnitude (decay-attenuated) converges.
func buildNonConvergedPath() *propagationCorpus {
	c := newPropagationCorpus()
	pathLen := 160
	for i := range pathLen {
		pol := ""
		switch i {
		case 0:
			pol = "positive"
		case pathLen - 1:
			pol = "negative"
		}
		c.addThought(fmt.Sprintf("p%d", i), pol)
	}
	for i := 0; i < pathLen-1; i++ {
		c.addEdge(fmt.Sprintf("p%d", i), fmt.Sprintf("p%d", i+1))
	}
	return c
}

// TestDeadband_NonConvergedComponentZeroWriteback (FAILS-WHEN-ABSENT for the
// per-component band) proves a non-converged component writes ZERO rows when the
// persisted values drift by less than that component's adaptive band. A reference
// applying pass learns the capped propagated_* and the component's valence residual
// R. Persisting the reference values perturbed by ONLY +R/2 on propagated_valence
// (magnitude converges and stays at reference) and running a full pass must write
// nothing: the component's adaptive band (≈2R) exceeds the R/2 drift. Under the
// FIXED 1e-4 band this fails — R/2 ≥ the floor (anti-vacuity guard R ≥ 2·floor), so
// every valence row is re-written.
func TestDeadband_NonConvergedComponentZeroWriteback(t *testing.T) {
	ctx := context.Background()

	// Reference full pass (applying) to learn the capped propagated_* and R.
	ref := &applyingPropagationCorpus{buildNonConvergedPath()}
	res, err := RunPropagationScoped(ctx, ref, nil, ref.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)
	require.Len(t, res.NonConverged, 1, "the long path must be the sole non-converged component")
	R := res.NonConverged[0].ValenceResidual
	require.GreaterOrEqual(t, R, 2*writebackDeadband,
		"anti-vacuity: the valence residual must exceed twice the floor so R/2 clears the fixed band")
	t.Logf("non-converged valence residual R = %g (2·floor = %g); perturbing valence by +R/2 = %g", R, 2*writebackDeadband, R/2)

	// Persist the reference values, perturbing ONLY propagated_valence by +R/2.
	// Magnitude converges on this fixture and stays at its reference value; a
	// both-keys perturbation would push magnitude supra-band and fail GREEN.
	scoped := buildNonConvergedPath()
	for id, n := range ref.thoughts {
		vv := parseFloat(kgtypes.Value(n, "propagated_valence")) + R/2
		kgtypes.SetValue(scoped.thoughts[id], "propagated_valence", fmt.Sprintf("%.6f", vv))
		kgtypes.SetValue(scoped.thoughts[id], "propagated_magnitude", kgtypes.Value(n, "propagated_magnitude"))
	}

	// Full pass over the same graph: the per-component band (≈2R) exceeds the R/2
	// drift, so no valence row is written; magnitude equals reference → none either.
	_, err = RunPropagationScoped(ctx, scoped, nil, scoped.nodeByIDFrom(), nil, nil)
	require.NoError(t, err)

	assert.Empty(t, scoped.capturedIDs("propagated_valence"),
		"no valence row is written when the per-component band (≈2R) exceeds the R/2 drift")
	assert.Empty(t, scoped.capturedIDs("propagated_magnitude"),
		"magnitude equals reference → no magnitude row is written")
}

// TestPropagatedValueChangedBand (FAILS-WHEN-ABSENT for the band predicate) pins the
// per-component band gate: the SAME drift crosses or clears depending on the band a
// component supplies. A 2e-3 drift writes under the fixed floor (1e-4) but is
// suppressed under a wide 3e-3 band; a 4e-3 drift exceeds that same wide band and
// writes; an unset persisted value always writes regardless of band (first-persist
// guard).
func TestPropagatedValueChangedBand(t *testing.T) {
	cases := []struct {
		name string
		cur  string
		want string
		band float64
		exp  bool
	}{
		{"drift_2e-3_under_floor_writes", "0.500000", "0.502000", 1e-4, true},     // 2e-3 >= 1e-4
		{"drift_2e-3_under_wide_band_skips", "0.500000", "0.502000", 3e-3, false}, // 2e-3 < 3e-3
		{"drift_4e-3_over_wide_band_writes", "0.500000", "0.504000", 3e-3, true},  // 4e-3 >= 3e-3
		{"unset_current_always_writes", "", "0.000000", 3e-3, true},               // first-persist guard
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.exp, propagatedValueChangedBand(tc.cur, tc.want, tc.band),
				"propagatedValueChangedBand(%q, %q, %g)", tc.cur, tc.want, tc.band)
		})
	}
}
