// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"
	"fmt"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDegreeHistogram verifies the canonical hub-and-spoke shape: a single hub
// node with out-degree 50 and 50 leaves each with in-degree 1. The histogram
// must show one node in the "26_100" total bucket and 50 nodes in the
// "bucket_1" in-degree bucket. Byte-stable vs the original store-backed fixture.
func TestDegreeHistogram(t *testing.T) {
	hubID := "hub"
	nodes := []*knowledgev1.Node{mkNode(hubID, "page", "hub")}
	var edges []*knowledgev1.Edge
	for i := range 50 {
		leafID := fmt.Sprintf("leaf-%d", i)
		nodes = append(nodes, mkNode(leafID, "page", fmt.Sprintf("leaf-%d", i)))
		edges = append(edges, refEdge(hubID, leafID))
	}
	f := &fakeCaller{nodes: nodes, edges: edges}

	a := DegreeHistogramAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, nil))
	require.NoError(t, err)
	require.Len(t, findings, 1, "degree-histogram emits one aggregate finding")

	fnd := findings[0]
	assert.Equal(t, "degree-histogram", fnd.Algorithm)
	assert.InDelta(t, 51.0, fnd.Metrics["total_nodes"], 1e-9)

	// 50 leaves each have in-degree 1 → bucket_1.
	assert.InDelta(t, 50.0, fnd.Metrics["in:bucket_1"], 1e-9,
		"50 leaves should land in in-degree bucket_1")
	// Hub has out-degree 50 → bucket_26_100.
	assert.InDelta(t, 1.0, fnd.Metrics["out:bucket_26_100"], 1e-9,
		"hub with out-degree 50 should land in out-degree bucket_26_100")
	// Hub total degree = 50 → bucket_26_100. Leaves total = 1 → bucket_1.
	assert.InDelta(t, 1.0, fnd.Metrics["total:bucket_26_100"], 1e-9)
	assert.InDelta(t, 50.0, fnd.Metrics["total:bucket_1"], 1e-9)

	// Evidence must be drawn from the highest-populated total-degree bucket —
	// that's the hub (bucket_26_100).
	require.NotEmpty(t, fnd.Evidence)
	assert.Equal(t, hubID, fnd.Evidence[0],
		"the hub node should be the first piece of Evidence (highest total-degree bucket)")
}

// TestDegreeHistogram_BucketBoundaries exercises the bucket range edges
// (0, 1, 2, 5→6 transition) so the label-assignment helper never drifts.
func TestDegreeHistogram_BucketBoundaries(t *testing.T) {
	for _, tc := range []struct {
		degree int
		want   string
	}{
		{0, "bucket_0"},
		{1, "bucket_1"},
		{2, "bucket_2"},
		{3, "bucket_3_5"},
		{5, "bucket_3_5"},
		{6, "bucket_6_10"},
		{10, "bucket_6_10"},
		{11, "bucket_11_25"},
		{25, "bucket_11_25"},
		{26, "bucket_26_100"},
		{100, "bucket_26_100"},
		{101, "bucket_101_1000"},
		{1000, "bucket_101_1000"},
		{1001, "bucket_1001_plus"},
		{50000, "bucket_1001_plus"},
	} {
		assert.Equal(t, tc.want, bucketLabel(tc.degree),
			"degree %d should map to %s", tc.degree, tc.want)
	}
}

// TestDegreeHistogram_Registered verifies init() self-registration.
func TestDegreeHistogram_Registered(t *testing.T) {
	a, ok := foundation.Get("degree-histogram")
	require.True(t, ok, "degree-histogram analyzer must be registered at package init")
	assert.Equal(t, "degree-histogram", a.Name())
}
