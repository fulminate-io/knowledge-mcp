// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// TestMaterializeLogGraph_ReturnsProxiesFromResolutions exercises the pure-
// transform shape of MaterializeLogGraph: a non-empty resolutions slice must
// produce both a NodeProxy in the returned nodes AND an EMITTED_BY edge
// from the corresponding log-label node to that proxy in the returned edges.
//
// Regression guard for the client-side flow: prior to the pure
// shape, the proxies were only written via the server-side
// writeProxiesFromResolutions DB-write call; the client could not derive
// them deterministically. This test fails if MaterializeLogGraph regresses
// to dropping resolutions on the floor.
func TestMaterializeLogGraph_ReturnsProxiesFromResolutions(t *testing.T) {
	// One LogStream carrying the service=api low-cardinality label so the
	// structural pass emits the log-label:service=api NodeLogLabel that the
	// EMITTED_BY edge's FromID references.
	streams := []*wirelogs.LogStream{{
		ID:     "stream-1",
		Labels: map[string]string{wirelogs.FieldService: "api"},
		LowCardLabels: map[string]string{
			wirelogs.FieldService: "api",
		},
		Fingerprint: "fp-1",
	}}

	resolutions := []wirelogs.ResolvedProxyEntry{{
		LabelKey:   wirelogs.FieldService,
		LabelValue: "api",
		Account:    "acct-1",
		ResourceID: "api-resource",
	}}

	nodes, edges, err := MaterializeLogGraph(
		"qid-1",
		nil, streams, nil, // templates, streams, chunks
		nil,         // correlations
		resolutions, // proxies
	)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "expected nodes in returned batch")
	require.NotEmpty(t, edges, "expected edges in returned batch")

	// Find the proxy node by its deterministic ID.
	wantProxyID := "proxy:cloud:acct-1:api-resource"
	var proxy *knowledgev1.Node
	for i := range nodes {
		if nodes[i].Id == wantProxyID {
			proxy = nodes[i]
			break
		}
	}
	require.NotNilf(t, proxy, "expected NodeProxy with id %s in returned nodes", wantProxyID)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "acct-1", kgtypes.Value(proxy, "account"))
	assert.Equal(t, "api-resource", kgtypes.Value(proxy, "foreign_id"))

	// Find the EMITTED_BY edge from the label node to the proxy.
	labelID := LabelNodeID(wirelogs.FieldService, "api")
	foundEmittedBy := false
	for _, e := range edges {
		if e.Type == kgtypes.EdgeEmittedBy && e.FromID == labelID && e.ToID == wantProxyID {
			foundEmittedBy = true
			break
		}
	}
	assert.True(t, foundEmittedBy,
		"expected an EMITTED_BY edge from %s to %s; got edges=%+v",
		labelID, wantProxyID, edges)
}

// TestMaterializeLogGraph_PreservesCorrelationConfidence is the regression
// guard for the BatchEdge.Confidence audit finding: confirmed correlations
// must yield CORRELATES_WITH BatchEdges whose Confidence field equals the
// cooccurrence score. The end-to-end Confidence preservation (BatchEdge →
// stored Edge) is covered by TestCompositeDBTxn_CreateBatch_PreservesConfidence
// in domains/store; this test pins the materializer half.
func TestMaterializeLogGraph_PreservesCorrelationConfidence(t *testing.T) {
	correlations := []wirelogs.CorrelationResult{{
		TemplateA:             "tplA",
		TemplateB:             "tplB",
		ServiceA:              "svcA",
		ServiceB:              "svcB",
		ResourceA:             "resA",
		ResourceB:             "resB",
		CooccurrenceScore:     0.42,
		StructurallyConfirmed: true,
	}}

	_, edges, err := MaterializeLogGraph("qid-corr", nil, nil, nil, correlations, nil)
	require.NoError(t, err)

	var seen bool
	for _, e := range edges {
		if e.Type != kgtypes.EdgeCorrelatesWith {
			continue
		}
		if e.FromID != "tplA" || e.ToID != "tplB" {
			continue
		}
		seen = true
		assert.InDelta(t, 0.42, e.Confidence, 1e-9, "BatchEdge.Confidence must carry CooccurrenceScore")
		assert.Equal(t, "temporal+cloud-dependency", e.Method)
		assert.Contains(t, e.Evidence, "services=svcA,svcB")
	}
	assert.True(t, seen, "expected a CORRELATES_WITH BatchEdge for tplA→tplB; got %+v", edges)
}

// TestMaterializeLogGraph_SkipsUnconfirmedCorrelations asserts that
// correlations without StructurallyConfirmed do NOT emit edges — they
// belong to the text summary only.
func TestMaterializeLogGraph_SkipsUnconfirmedCorrelations(t *testing.T) {
	correlations := []wirelogs.CorrelationResult{{
		TemplateA:             "tplA",
		TemplateB:             "tplB",
		StructurallyConfirmed: false,
	}}

	_, edges, err := MaterializeLogGraph("qid-corr", nil, nil, nil, correlations, nil)
	require.NoError(t, err)
	for _, e := range edges {
		assert.NotEqualf(t, kgtypes.EdgeCorrelatesWith, e.Type,
			"unconfirmed correlation must not emit a CORRELATES_WITH edge")
	}
}
