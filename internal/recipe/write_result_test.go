// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// captureSink is a capturing fake collector.Sink: it records every WriteResult
// call so tests can assert the shipped graph type / name / nodes / edges. NEVER
// touches a store.
type captureSink struct {
	calls   int
	names   []string
	results []*collectorwire.CollectResult
	err     error
}

func (s *captureSink) WriteResult(_ context.Context, collectorName string, result *collectorwire.CollectResult) error {
	s.calls++
	s.names = append(s.names, collectorName)
	s.results = append(s.results, result)
	return s.err
}

func TestWriteResult_ShipsToPracticeTarget(t *testing.T) {
	target := TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"}
	res := &Result{
		Nodes: []*knowledgev1.Node{{Id: "n1", Type: "pattern"}, {Id: "n2", Type: "pattern"}},
		Edges: []kgwire.BatchEdge{
			{FromIdx: -1, ToIdx: -1, FromID: "n1", ToID: "n2", Type: "relates-to", Method: "recipe"},
		},
		Lineage: []kgwire.BatchEdge{
			{FromIdx: -1, ToIdx: -1, FromID: "n1", ToID: "src1", Type: kgtypes.EdgeTranslatedFrom},
		},
	}

	sink := &captureSink{}
	require.NoError(t, writeResult(context.Background(), sink, target, res))

	require.Equal(t, 1, sink.calls, "exactly one WriteResult")
	assert.Equal(t, "recipe", sink.names[0])
	got := sink.results[0]
	assert.Equal(t, kgtypes.GraphPractice, got.GraphType)
	assert.Equal(t, "design-patterns", got.GraphName)
	assert.Len(t, got.Nodes, 2)
	// Edges carry both the structure edge AND the lineage edge.
	require.Len(t, got.Edges, 2)
	assert.Equal(t, kgtypes.EdgeType("relates-to"), got.Edges[0].Type)
	assert.Equal(t, kgtypes.EdgeTranslatedFrom, got.Edges[1].Type)
}

func TestWriteResult_NilSinkErrors(t *testing.T) {
	err := writeResult(context.Background(), nil, TargetSpec{}, &Result{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sink unavailable")
}
