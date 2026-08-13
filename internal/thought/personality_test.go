// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// chargeFakeCaller answers the two Execute round-trips fetchChargesFor issues:
//  1. a RETURN_MODE_EDGES query (Ids = thought set, Selection.EdgeTypes =
//     [charged-by]) → EdgeChargedBy edges (thought → charge); answered from
//     chargedBy (thoughtID → []chargeID).
//  2. an ids[] hydrate query → the charge nodes; answered from chargeNodes.
//
// It is a minimal purpose-built fake for the trust-math test: it never touches
// the wire, just maps the canned thought→charge edges and charge-node payloads.
type chargeFakeCaller struct {
	chargedBy   map[string][]string          // thoughtID → charge IDs (EdgeChargedBy)
	chargeNodes map[string]*knowledgev1.Node // chargeID → charge node
}

func (c *chargeFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Edge read: RETURN_MODE_EDGES over the requested thought-id set.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		var edges []*knowledgev1.Edge
		for _, tid := range q.GetIds() {
			for _, cid := range c.chargedBy[tid] {
				edges = append(edges, &knowledgev1.Edge{
					Type:   string(kgtypes.EdgeChargedBy),
					FromId: tid,
					ToId:   cid,
				})
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: edges}, nil
	}
	// Node hydrate: ids[] → charge nodes.
	var nodes []*knowledgev1.Node
	for _, id := range q.GetIds() {
		if n, ok := c.chargeNodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// chargeNode builds a charge node carrying the polarity/weight metadata
// buildChargeCache reads (weight encoded "%.2f" like the charge composer), plus a
// CreatedAt (unix-nanos) for subsequent-charge ordering.
func chargeNode(id, polarity string, weight float64, createdAt int64) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), CreatedAt: createdAt}
	kgtypes.SetValue(n, "polarity", polarity)
	kgtypes.SetValue(n, "weight", fmt.Sprintf("%.2f", weight))
	return n
}

// TestPersonalityScalars_RerinforcerSemantics drives ComputePersonalityScalars
// directly over a synthetic cluster corpus, covering all four re-enforcer trust
// assertions. The fake Caller serves the charges_for bulk read; evidenceAdj is
// passed directly (cases b/c) since it is a direct parameter.
func TestPersonalityScalars_RerinforcerSemantics(t *testing.T) {
	ctx := context.Background()

	// CASE (a): non-evidenced charges still differentiate trust off 1.000 via
	// the universal charged-by base leg — ZERO evidenced-by edges present.
	t.Run("non_evidenced_differentiates", func(t *testing.T) {
		clusters := []ThoughtCluster{
			{ID: "A", Label: "A", ThoughtIDs: []string{"tA1"}},
			{ID: "B", Label: "B", ThoughtIDs: []string{"tB1"}},
		}
		fc := &chargeFakeCaller{
			chargedBy: map[string][]string{"tA1": {"cA1", "cA2"}},
			chargeNodes: map[string]*knowledgev1.Node{
				"cA1": chargeNode("cA1", "positive", 5, 100),
				"cA2": chargeNode("cA2", "positive", 4, 200),
			},
		}
		// evidenceAdj = nil: no evidence resolution at all.
		profile, err := ComputePersonalityScalars(ctx, fc, clusters, nil, nil)
		require.NoError(t, err)
		// A→B: both charges confirmed (cA1's subsequent cA2 is same polarity;
		// cA2 no-subsequent fallback) → accuracy 1.0 → scalar 1.8, != 1.000.
		assert.NotEqual(t, 1.0, profile.Scalars["A"]["B"],
			"non-evidenced charges must shift A's scalar off the 1.000 default via the charged-by leg")
	})

	// CASE (b): an evidence-backed charge moves its pair scalar MORE than an
	// otherwise-identical non-evidenced one, by a MODEST margin. The evidence
	// charge is NET-CONFIRMING (a single positive charge with no contradicting
	// subsequent → track-record ratio 1.0) so boosting its confirmed leg raises
	// the pair's accuracy strictly (the boost scales confirmed AND contradicted,
	// so a balanced fixture would tie).
	t.Run("evidence_reinforces_modestly", func(t *testing.T) {
		clusters := []ThoughtCluster{
			{ID: "A", Label: "A", ThoughtIDs: []string{"tA1"}},
			{ID: "B", Label: "B", ThoughtIDs: []string{"tB1"}},
		}
		// c1 positive@100, c2 negative@200, c3 positive@300.
		// c1: subsequent c2(opp)→contradicted+=4, c3(same)→confirmed+=4.
		// c2: subsequent c3(opp)→contradicted+=4.
		// c3: no subsequent → confirmed += 4*0.5 = 2.  (c3 is net-confirming)
		// base totals: confirmed=4+2=6, contradicted=4+4=8 → accuracy 6/14.
		nodes := map[string]*knowledgev1.Node{
			"c1": chargeNode("c1", "positive", 4, 100),
			"c2": chargeNode("c2", "negative", 4, 200),
			"c3": chargeNode("c3", "positive", 4, 300),
		}
		fc := &chargeFakeCaller{
			chargedBy:   map[string][]string{"tA1": {"c1", "c2", "c3"}},
			chargeNodes: nodes,
		}
		without, err := ComputePersonalityScalars(ctx, fc, clusters, nil, nil)
		require.NoError(t, err)
		// Evidence: c3 (the net-confirming charge) targets a clustered B thought.
		with, err := ComputePersonalityScalars(ctx, fc, clusters, map[string][]string{"c3": {"tB1"}}, nil)
		require.NoError(t, err)

		sWithout := without.Scalars["A"]["B"]
		sWith := with.Scalars["A"]["B"]
		assert.Greater(t, sWith, sWithout,
			"a net-confirming evidence-backed charge must raise the pair scalar")
		// MODEST: the delta must be far smaller than the full scalar range (1.6).
		// Boosting one charge's confirmed contribution by 0.2x cannot dominate.
		delta := sWith - sWithout
		assert.Less(t, delta, 0.2,
			"the re-enforcement delta must be modest (boost must not dominate caller weight)")
		assert.Greater(t, delta, 0.0)
	})

	// CASE (c): thought-targeted evidence drives cross-cluster pair attribution —
	// the boosted A→B pair is sharpened relative to the un-attributed A→C pair.
	t.Run("thought_evidence_drives_pair_attribution", func(t *testing.T) {
		clusters := []ThoughtCluster{
			{ID: "A", Label: "A", ThoughtIDs: []string{"tA1"}},
			{ID: "B", Label: "B", ThoughtIDs: []string{"tB1"}},
			{ID: "C", Label: "C", ThoughtIDs: []string{"tC1"}},
		}
		nodes := map[string]*knowledgev1.Node{
			"c1": chargeNode("c1", "positive", 4, 100),
			"c2": chargeNode("c2", "negative", 4, 200),
			"c3": chargeNode("c3", "positive", 4, 300),
		}
		fc := &chargeFakeCaller{
			chargedBy:   map[string][]string{"tA1": {"c1", "c2", "c3"}},
			chargeNodes: nodes,
		}
		// c3's evidence targets a B thought → A→B is boosted, A→C is not.
		profile, err := ComputePersonalityScalars(ctx, fc, clusters, map[string][]string{"c3": {"tB1"}}, nil)
		require.NoError(t, err)
		assert.NotEqual(t, profile.Scalars["A"]["B"], profile.Scalars["A"]["C"],
			"thought-targeted evidence to B must sharpen A→B relative to the un-attributed A→C")
	})

	// CASE (d): zero charges → every pair scalar is the 1.000 default
	// (computeClusterPairScalar total==0 → 1.0).
	t.Run("zero_charges_defaults_to_1", func(t *testing.T) {
		clusters := []ThoughtCluster{
			{ID: "A", Label: "A", ThoughtIDs: []string{"tA1"}},
			{ID: "B", Label: "B", ThoughtIDs: []string{"tB1"}},
		}
		fc := &chargeFakeCaller{} // no charges at all.
		profile, err := ComputePersonalityScalars(ctx, fc, clusters, nil, nil)
		require.NoError(t, err)
		assert.InDelta(t, 1.0, profile.Scalars["A"]["B"], 1e-9)
		assert.InDelta(t, 1.0, profile.Scalars["B"]["A"], 1e-9)
	})
}
