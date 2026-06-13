// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recordingCoverageStatsRPC records the StatsRequest it received so the test can
// assert IncludeCoverage was set, and returns a scripted GraphStats.
type recordingCoverageStatsRPC struct {
	lastReq *knowledgev1.StatsRequest
	stats   *knowledgev1.GraphStats
}

func (r *recordingCoverageStatsRPC) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	r.lastReq = req
	return &knowledgev1.StatsResponse{GraphStats: r.stats}, nil
}

func (r *recordingCoverageStatsRPC) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// nonStatsGraphCaller satisfies GraphCaller but NOT statsRPC (no Stats method) —
// the router-less / degraded-mode case.
type nonStatsGraphCaller struct{}

func (nonStatsGraphCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestGraphEmbeddedCount is the shared-helper criterion: GraphEmbeddedCount issues
// ONE Stats RPC with IncludeCoverage:true and returns GraphStats.BinaryVectorCount,
// and returns (0,nil) when the caller does not satisfy the Stats seam.
func TestGraphEmbeddedCount(t *testing.T) {
	ctx := context.Background()

	rec := &recordingCoverageStatsRPC{stats: &knowledgev1.GraphStats{BinaryVectorCount: 4096}}
	got, err := GraphEmbeddedCount(ctx, rec, kgtypes.GraphCode, "repoX")
	require.NoError(t, err)
	require.Equal(t, 4096, got, "returns GraphStats.BinaryVectorCount")
	require.NotNil(t, rec.lastReq, "issued a Stats RPC")
	require.True(t, rec.lastReq.GetIncludeCoverage(), "the Stats RPC sets IncludeCoverage")
	require.Equal(t, "code", rec.lastReq.GetTarget().GetGraph())
	require.Equal(t, "repoX", rec.lastReq.GetTarget().GetRepo())

	// Default knowledge graph (empty name) uses the empty-name selector.
	recKG := &recordingCoverageStatsRPC{stats: &knowledgev1.GraphStats{BinaryVectorCount: 10}}
	_, err = GraphEmbeddedCount(ctx, recKG, kgtypes.GraphKnowledge, "")
	require.NoError(t, err)
	require.Empty(t, recKG.lastReq.GetTarget().GetGraph(), "default knowledge graph uses empty-name selector")

	// A caller that does not satisfy the Stats seam → (0, nil), no panic.
	got, err = GraphEmbeddedCount(ctx, nonStatsGraphCaller{}, kgtypes.GraphCode, "repoX")
	require.NoError(t, err)
	require.Equal(t, 0, got, "a non-stats caller yields a zero embedded count")
}
