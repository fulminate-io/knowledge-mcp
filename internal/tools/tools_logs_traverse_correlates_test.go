// SPDX-License-Identifier: Apache-2.0

// Package tools — stream-correlation traverse tests.
//
// These exercise writeStreamCorrelations end-to-end by traversing a
// stream after seeding a CORRELATES_WITH edge between two of the
// stream's templates. The synthetic pipeline fixture in
// tools_logs_query_test.go never emits correlations naturally (no
// dependency checker in the test loop), so the seed-then-traverse
// pattern mirrors the correlations-mode tests.
package tools

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestStreamTraverse_IncludesCorrelatedTemplates asserts the
// "Correlated templates" section renders when the stream's templates
// have CORRELATES_WITH edges.
func TestStreamTraverse_IncludesCorrelatedTemplates(t *testing.T) {
	queryID := "q-stream-corr"
	nodes, edges := buildLogCorpus(t, queryID)

	// Pick a stream + the template of one of its chunks so we can seed a
	// correlation that involves this stream via shared template ID.
	streamID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogStream)
	var myTemplateID string
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeLogChunk && kgtypes.Value(n, "stream_id") == streamID {
			myTemplateID = kgtypes.Value(n, "template_id")
			break
		}
	}
	require.NotEmpty(t, myTemplateID, "expected at least one chunk for the stream")

	// Find a second, distinct template to seed as the correlation peer.
	var peerTemplateID string
	for _, id := range templateNodeIDs(nodes) {
		if id != myTemplateID {
			peerTemplateID = id
			break
		}
	}
	require.NotEmpty(t, peerTemplateID, "need ≥2 templates to seed a correlation")

	// Seed the CORRELATES_WITH edge into the corpus the fake serves.
	edges = append(edges, &knowledgev1.Edge{
		FromId:     myTemplateID,
		ToId:       peerTemplateID,
		Type:       string(kgtypes.EdgeCorrelatesWith),
		Confidence: 0.88,
		Method:     "test",
		Evidence:   "services=myservice,peerservice resources=pod/me,pod/you score=0.880",
	})

	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: queryID, Start: streamID, Direction: "both",
	})
	require.False(t, result.IsError, "stream traverse: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log stream", "stream header should render")
	// Streams in the synthetic fixture have service=api / service=db /
	// service=worker. The seeded edge uses services=myservice,peerservice
	// which doesn't match the stream's service, so the section renders as
	// "Shared-template correlations" rather than "Correlated with".
	assert.Contains(t, text,
		"Shared-template correlations",
		"section should render even when seeded services don't match the stream's service")
	assert.Contains(t, text, "0.88",
		"seeded score should appear in the section")
	assert.Contains(t, text, "myservice",
		"side A service should appear")
	assert.Contains(t, text, "peerservice",
		"side B service should appear")
}

// TestStreamTraverse_NoCorrelationsSilent asserts the correlated-
// templates section is omitted entirely when the stream's templates
// have no CORRELATES_WITH edges. We don't want a noisy "(none)" line.
func TestStreamTraverse_NoCorrelationsSilent(t *testing.T) {
	queryID := "q-stream-no-corr"
	nodes, _ := buildLogCorpus(t, queryID)
	streamID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogStream)

	h := setupLogTestHandler(t, queryID)
	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: queryID, Start: streamID, Direction: "both",
	})
	require.False(t, result.IsError)
	text := resultText(result)

	assert.Contains(t, text, "Log stream", "baseline section should still render")
	assert.NotContains(t, text, "Correlated with",
		"no correlations → direct section should be absent")
	assert.NotContains(t, text, "Shared-template correlations",
		"no correlations → indirect section should be absent")
}

// TestUniqueTemplateIDsFromChunks asserts dedup across multiple chunks
// with the same template_id, and skip for chunks missing the meta.
func TestUniqueTemplateIDsFromChunks(t *testing.T) {
	chunks := []*knowledgev1.Node{
		{Id: "c1", Metadata: map[string]string{"template_id": "tplA"}},
		{Id: "c2", Metadata: map[string]string{"template_id": "tplB"}},
		{Id: "c3", Metadata: map[string]string{"template_id": "tplA"}},
		// dup
		{Id: "c4", Metadata: map[string]string{}},
		// no template_id,
	}
	ids := uniqueTemplateIDsFromChunks(chunks)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "tplA")
	assert.Contains(t, ids, "tplB")
}

// TestSortStreamCorrelations_OrderDescByScore asserts rows are sorted
// by score desc with alias tiebreak.
func TestSortStreamCorrelations_OrderDescByScore(t *testing.T) {
	rows := []streamCorrelation{
		{MyTemplate: &knowledgev1.Node{Id: "m1", Metadata: map[string]string{"alias": "m-low"}},
			PeerTemplate: &knowledgev1.Node{Id: "p1", Metadata: map[string]string{"alias": "z-peer"}},
			Score:        0.5},
		{MyTemplate: &knowledgev1.Node{Id: "m2", Metadata: map[string]string{"alias": "m-high"}},
			PeerTemplate: &knowledgev1.Node{Id: "p2", Metadata: map[string]string{"alias": "a-peer"}},
			Score:        0.9},
		{MyTemplate: &knowledgev1.Node{Id: "m3"},
			PeerTemplate: &knowledgev1.Node{Id: "p3", Metadata: map[string]string{"alias": "m-peer"}},
			Score:        0.9}, // tied score
	}
	sortStreamCorrelations(rows)
	// 0.9 / alias "a-peer" should come before 0.9 / alias "m-peer"
	// because ties break on peer alias ascending.
	assert.InDelta(t, 0.9, rows[0].Score, 1e-9)
	assert.Equal(t, "a-peer", kgtypes.Value(rows[0].PeerTemplate, "alias"))
	assert.InDelta(t, 0.9, rows[1].Score, 1e-9)
	assert.Equal(t, "m-peer", kgtypes.Value(rows[1].PeerTemplate, "alias"))
	assert.InDelta(t, 0.5, rows[2].Score, 1e-9)
}

// TestTemplateRefShort covers the alias → SymbolName → short-hex
// fallback chain.
func TestTemplateRefShort(t *testing.T) {
	withAlias := knowledgev1.Node{Metadata: map[string]string{"alias": "my-alias@err"}}
	assert.Equal(t, "`my-alias@err`", templateRefShort(&withAlias))

	withSymbol := knowledgev1.Node{SymbolName: "fallback-symbol"}
	assert.Equal(t, "`fallback-symbol`", templateRefShort(&withSymbol))

	bareHex := knowledgev1.Node{Id: "abcdef1234567890abcdef"}
	got := templateRefShort(&bareHex)
	assert.True(t, strings.HasPrefix(got, "`abcdef12"),
		"bare-hex should fall back to 8-char short hex, got: %s", got)
}

// TestServiceOrDash covers the empty-string → "—" replacement.
func TestServiceOrDash(t *testing.T) {
	assert.Equal(t, "—", serviceOrDash(""))
	assert.Equal(t, "api", serviceOrDash("api"))
}

// TestPartitionCorrelationsByStream asserts the direct/indirect split:
// rows where myService matches either side go to direct; the rest go
// to indirect. Empty myService treats every row as indirect.
func TestPartitionCorrelationsByStream(t *testing.T) {
	rows := []streamCorrelation{
		{ServiceA: "api", ServiceB: "db"},
		{ServiceA: "worker", ServiceB: "api"},
		{ServiceA: "foo", ServiceB: "bar"},
	}
	direct, indirect := partitionCorrelationsByStream(rows, "api")
	assert.Len(t, direct, 2, "two rows should match service=api on either side")
	assert.Len(t, indirect, 1, "one row has neither side matching")
	assert.Equal(t, "foo", indirect[0].ServiceA)

	// Empty service → everything is indirect (no anchor to match on).
	allDirect, allIndirect := partitionCorrelationsByStream(rows, "")
	assert.Empty(t, allDirect)
	assert.Len(t, allIndirect, len(rows))
}
