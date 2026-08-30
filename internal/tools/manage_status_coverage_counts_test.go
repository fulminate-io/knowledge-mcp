// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recordingStatsCaller counts Stats calls and records the last request, so a test
// can assert HOW MANY reads a helper made rather than only what it returned.
type recordingStatsCaller struct {
	GraphCaller
	calls int
	last  *knowledgev1.StatsRequest
	resp  *knowledgev1.GraphStats
}

func (r *recordingStatsCaller) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	r.calls++
	r.last = req
	return &knowledgev1.StatsResponse{GraphStats: r.resp}, nil
}

// TestGraphCoverageCounts_SingleStatsCall pins the property the whole widening
// exists for: every coverage number comes from ONE read.
//
// THE CALL-COUNT ASSERTION IS THE POINT, not decoration. Two Stats calls would
// return two snapshots of a graph that is being written to concurrently, so a
// consumer comparing an embedded count from one against a failure count from the
// other is comparing numbers that were never true at the same instant. It is also
// what stops a future second helper from forking the read: a fork shows up here as
// calls==2 long before it shows up as a wrong answer.
func TestGraphCoverageCounts_SingleStatsCall(t *testing.T) {
	rec := &recordingStatsCaller{resp: &knowledgev1.GraphStats{
		NodeCount:           1000,
		BinaryVectorCount:   940,
		SummarizedCount:     900,
		SummaryFailureCount: 7,
		EmbedFailureCount:   3,
		NonProxyNodeCount:   980,
	}}

	got, err := GraphCoverageCounts(context.Background(), rec, kgtypes.GraphCode, "repo")
	require.NoError(t, err)

	require.Equal(t, 1, rec.calls,
		"the whole coverage set must come from exactly ONE Stats read — two reads are two "+
			"snapshots, and a consumer comparing a count from each is comparing numbers that "+
			"were never simultaneously true")
	require.NotNil(t, rec.last)
	require.True(t, rec.last.GetIncludeCoverage(),
		"IncludeCoverage must be set, or the response carries none of the four fields below")

	// ALL SIX come back, with the fake's values. Distinct values per field, so a
	// mis-wired field (embedded read into summarized, say) cannot pass.
	require.Equal(t, 1000, got.Nodes)
	require.Equal(t, 940, got.Embedded)
	require.Equal(t, 900, got.Summarized)
	require.Equal(t, 7, got.SummaryFailures)
	require.Equal(t, 3, got.EmbedFailures)
	require.Equal(t, 980, got.NonProxyNodes)
	require.True(t, got.Measurable, "a successful read is measurable")

	// THE UN-MEASURABLE CASE stays distinguishable from a measured zero. Without
	// this, a caller with no stats seam reads six confident zeros.
	noSeam, err := GraphCoverageCounts(context.Background(), struct{ GraphCaller }{}, kgtypes.GraphCode, "repo")
	require.NoError(t, err, "a caller with no stats seam is not an error")
	require.False(t, noSeam.Measurable,
		"a caller that cannot be measured must say so; six zeros and Measurable=true would "+
			"read as a graph that genuinely has nothing")
	require.Zero(t, noSeam.Embedded)
}
