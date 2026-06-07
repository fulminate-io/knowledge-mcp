// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// ToCollectResult converts the plain-Go envelope the external binary emitted
// into the in-tree wire payload (collectorwire.CollectResult) the UploadSink
// ships to the server. It builds *knowledgev1.Node literals from the envelope's
// plain Node fields and kgwire.BatchEdge values from the envelope's plain Edge
// fields, then stamps GraphType/GraphName from the envelope.
//
// It does NOT sanitize node text: the UploadSink already runs sanitizeNodeText
// over every node before marshal (collector/remote/sink.go), so duplicating it
// here would be redundant work.
//
// Validation fails LOUD on a malformed envelope — empty graph_type, empty
// graph_name, or a node with an empty type all return a non-nil error rather
// than silently shipping a degenerate result. A registered collector that
// prints garbage must surface as a collect error, never a silent no-op.
func (r *Result) ToCollectResult() (*collectorwire.CollectResult, error) {
	if r == nil {
		return nil, fmt.Errorf("externalcollector: nil Result")
	}
	if r.GraphType == "" {
		return nil, fmt.Errorf("externalcollector: envelope graph_type is empty")
	}
	if r.GraphName == "" {
		return nil, fmt.Errorf("externalcollector: envelope graph_name is empty")
	}

	nodes := make([]*knowledgev1.Node, 0, len(r.Nodes))
	for i := range r.Nodes {
		n := &r.Nodes[i]
		if n.Type == "" {
			return nil, fmt.Errorf("externalcollector: node[%d] (id=%q) has an empty type", i, n.ID)
		}
		nodes = append(nodes, &knowledgev1.Node{
			Id:          n.ID,
			Type:        n.Type,
			SymbolName:  n.SymbolName,
			FilePath:    n.FilePath,
			Language:    n.Language,
			StartLine:   int32(n.StartLine),
			EndLine:     int32(n.EndLine),
			Content:     n.Content,
			Signature:   n.Signature,
			Summary:     n.Summary,
			Description: n.Description,
			Source:      n.Source,
			Status:      n.Status,
			Keywords:    n.Keywords,
			IsExported:  n.IsExported,
			Metadata:    n.Metadata,
		})
	}

	edges := make([]kgwire.BatchEdge, 0, len(r.Edges))
	for i := range r.Edges {
		e := &r.Edges[i]
		edges = append(edges, kgwire.BatchEdge{
			// The external contract references endpoints by node ID; the
			// index form is internal to the chunker, so -1 selects the ID.
			FromIdx:    -1,
			ToIdx:      -1,
			FromID:     e.FromID,
			ToID:       e.ToID,
			Type:       kgtypes.EdgeType(e.Type),
			Weight:     e.Weight,
			Confidence: e.Confidence,
			Method:     e.Method,
			Evidence:   e.Evidence,
		})
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphType(r.GraphType),
		GraphName: r.GraphName,
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}
