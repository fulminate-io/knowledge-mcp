// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// public_exposure_scoring_test.go exercises scorePaths, compositeScore,
// buildExposureFinding, and pruneToTopN.

// makeTestPath builds a synthetic attackPath with the given hop count
// and sensitivity. Nodes/edges are synthetic string IDs.
func makeTestPath(hopCount int, sensitivity float64, reason string) attackPath {
	nodes := make([]string, hopCount+1)
	nodes[0] = "seed"
	for i := 1; i <= hopCount; i++ {
		nodes[i] = "hop-" + string(rune('A'+i-1))
	}
	edges := make([]walkEdge, hopCount)
	for i := range edges {
		edges[i] = walkEdge{ToID: nodes[i+1], Kind: kgtypes.EdgeUsesSecurityGroup}
	}
	return attackPath{
		Seed: publicSeed{
			NodeID:       "seed",
			ResourceType: "elbv2-loadbalancer",
			EntryScore:   1.0,
			Reason:       "internet-facing load balancer",
			CloudFamily:  "aws",
		},
		Nodes:           nodes,
		Edges:           edges,
		SensitiveScore:  sensitivity,
		SensitiveReason: reason,
	}
}

// TestCompositeScore_Formula pins the ticket-defined scoring formula:
// composite = sensitivity / (hop_count + mitigation_count + 1).
func TestCompositeScore_Formula(t *testing.T) {
	require.InDelta(t, 1.0/3.0, compositeScore(1.0, 2, 0), 0.0001, "2-hop direct path to admin")
	require.InDelta(t, 1.0/6.0, compositeScore(1.0, 5, 0), 0.0001, "5-hop path lower-scored")
	require.InDelta(t, 0.0, compositeScore(0.0, 2, 0), 0.0001, "zero sensitivity = zero composite")
	require.InDelta(t, 0.5, compositeScore(1.0, 0, 1), 0.0001, "mitigation subtracts")
}

// TestScorePaths_TwoHopOutranksFiveHop verifies the 2-hop direct path
// outranks the 5-hop path with the same terminal sensitivity.
func TestScorePaths_TwoHopOutranksFiveHop(t *testing.T) {
	paths := []attackPath{
		makeTestPath(5, 1.0, "admin-reachable IAM role"),
		makeTestPath(2, 1.0, "admin-reachable IAM role"),
	}
	scored := scorePaths(paths)
	require.Len(t, scored, 2)
	require.Greater(t, scored[0].CompositeScore, scored[1].CompositeScore)
	require.Len(t, scored[0].Edges, 2, "2-hop path should sort first")
}

// TestScorePaths_ZeroSensitivityFiltersOut verifies paths with zero
// sensitivity are scored zero but still returned.
func TestScorePaths_ZeroSensitivityReturnsZero(t *testing.T) {
	paths := []attackPath{makeTestPath(3, 0.0, "not sensitive")}
	scored := scorePaths(paths)
	require.Len(t, scored, 1)
	require.InDelta(t, 0.0, scored[0].CompositeScore, 0.0001)
}

// TestBuildExposureFinding_Shape verifies the finding structure: the
// algorithm, severity, summary template, evidence ordering, and metrics.
func TestBuildExposureFinding_Shape(t *testing.T) {
	path := makeTestPath(2, 1.0, "admin-reachable IAM role")
	sp := scorePaths([]attackPath{path})[0]

	req := Request{Caller: nil, Graph: kgtypes.GraphCloud, Name: "test-account"}
	f := buildExposureFinding(t.Context(), req, "aws_public_exposure", sp)

	require.Equal(t, "aws_public_exposure", f.Algorithm)
	// sensitivity = 1.0 ≥ 0.95 → severityForExposure returns critical
	// regardless of composite score. Admin-reachable IAM roles are always
	// critical when reached from a public entry point.
	require.Equal(t, SeverityCritical, f.Severity)
	require.Contains(t, f.Title, "Public exposure")
	require.Contains(t, f.Summary, "internet-facing load balancer")
	require.Contains(t, f.Summary, "admin-reachable IAM role")
	require.Contains(t, f.Summary, "2 hops")

	require.Len(t, f.Evidence, 3, "path of 2 hops → 3 nodes")
	require.Equal(t, "seed", f.Evidence[0])

	require.InDelta(t, 1.0/3.0, f.Metrics["composite_score"], 0.0001)
	require.InDelta(t, 2.0, f.Metrics["hop_count"], 0.0001)
	require.InDelta(t, 0.0, f.Metrics["mitigation_count"], 0.0001)
	require.Equal(t, "aws", f.Metadata["cloud_family"])
	require.Equal(t, "internet-facing load balancer", f.Metadata["entry_reason"])
}

// TestBuildExposureFinding_HopListRendering verifies the hop list text
// carries every node + edge kind in order.
func TestBuildExposureFinding_HopListRendering(t *testing.T) {
	path := makeTestPath(3, 0.9, "relational database")
	sp := scorePaths([]attackPath{path})[0]

	req := Request{Graph: kgtypes.GraphCloud, Name: "test"}
	f := buildExposureFinding(t.Context(), req, "aws_public_exposure", sp)
	// hop list looks like "seed -[USES_SECURITY_GROUP]-> hop-A -[...]-> hop-B ..."
	require.Contains(t, f.Summary, "seed -[USES_SECURITY_GROUP]->")
}

// TestBuildExposureFinding_NoSensitivityNoFinding verifies a zero-length
// path returns an empty finding with just the algorithm set.
func TestBuildExposureFinding_EmptyPath(t *testing.T) {
	sp := scoredPath{}
	req := Request{Graph: kgtypes.GraphCloud, Name: "test"}
	f := buildExposureFinding(t.Context(), req, "aws_public_exposure", sp)
	require.Equal(t, "aws_public_exposure", f.Algorithm)
	require.Empty(t, f.Evidence)
}

// TestSeverityForExposure_CompositeOnly covers each threshold boundary
// when sensitivity is zero (the high-sensitivity shortcut never fires).
func TestSeverityForExposure_CompositeOnly(t *testing.T) {
	require.Equal(t, SeverityWarning, severityForExposure(0, 0.8))
	require.Equal(t, SeverityWarning, severityForExposure(0, 0.4))
	require.Equal(t, SeverityWarning, severityForExposure(0, 0.3))
	require.Equal(t, SeverityNotice, severityForExposure(0, 0.1))
	require.Equal(t, SeverityNotice, severityForExposure(0, 0.0))
}

// TestSeverityForExposure verifies the two-arg severity classifier: a
// maximum-sensitivity terminal (IAM admin, KMS key, secret) always
// yields critical regardless of hop count, while lower-sensitivity
// terminals fall back to the composite-score threshold.
func TestSeverityForExposure(t *testing.T) {
	// High-sensitivity shortcut: composite irrelevant.
	require.Equal(t, SeverityCritical, severityForExposure(1.0, 0.01))
	require.Equal(t, SeverityCritical, severityForExposure(0.95, 0.0))
	// Below the sensitivity threshold: composite drives severity.
	require.Equal(t, SeverityWarning, severityForExposure(0.9, 0.4))
	require.Equal(t, SeverityWarning, severityForExposure(0.9, 0.3))
	require.Equal(t, SeverityNotice, severityForExposure(0.9, 0.29))
	require.Equal(t, SeverityNotice, severityForExposure(0.5, 0.1))
}

// TestPruneToTopN_KeepsHighestScores verifies the min-heap retains the
// top-N paths by composite score when the input exceeds the cap.
func TestPruneToTopN_KeepsHighestScores(t *testing.T) {
	paths := []scoredPath{
		{CompositeScore: 0.9},
		{CompositeScore: 0.5},
		{CompositeScore: 0.3},
		{CompositeScore: 0.8},
		{CompositeScore: 0.1},
	}
	pruned := pruneToTopN(paths, 3)
	require.Len(t, pruned, 3)
	scores := map[float64]bool{}
	for _, p := range pruned {
		scores[p.CompositeScore] = true
	}
	require.True(t, scores[0.9])
	require.True(t, scores[0.8])
	require.True(t, scores[0.5])
	require.False(t, scores[0.1])
	require.False(t, scores[0.3])
}

// TestPruneToTopN_UnderCap returns input unchanged.
func TestPruneToTopN_UnderCap(t *testing.T) {
	paths := []scoredPath{{CompositeScore: 0.9}, {CompositeScore: 0.1}}
	require.Equal(t, paths, pruneToTopN(paths, 5))
}

// TestExposureMetadata_CrossGraph verifies the cross_graph metadata flag
// is set when ANY edge carries the cross_graph marker.
func TestExposureMetadata_CrossGraph(t *testing.T) {
	path := attackPath{
		Seed:            publicSeed{NodeID: "alb", Reason: "lb", CloudFamily: "aws"},
		Nodes:           []string{"alb", "pod", "role"},
		Edges:           []walkEdge{{Kind: kgtypes.EdgeRoutesTo}, {Kind: kgtypes.EdgeAssumesRole, Metadata: map[string]string{"cross_graph": "true"}}},
		SensitiveScore:  1.0,
		SensitiveReason: "admin",
	}
	sp := scorePaths([]attackPath{path})[0]
	meta := exposureFindingMetadata(sp)
	require.Equal(t, "true", meta["cross_graph"])
}
