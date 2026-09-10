// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// ToCollectResult converts the plain-Go envelope the provider returned into the
// in-tree wire payload (collectorwire.CollectResult) the UploadSink ships to the
// server. It builds *knowledgev1.Node literals from the envelope's plain Node
// fields and kgwire.BatchEdge values from the envelope's plain Edge fields, then
// stamps the CLIENT-DERIVED graph family and instance.
//
// graphType IS THE REGISTRATION NAME and graphName IS THE COLLECT ID. Neither
// comes from the provider: under the MCP contract the result carries no graph
// identity at all, so there is nothing for a provider to cross into another
// graph type and no pin to enforce. The guards below therefore assert on the
// CLIENT's own values, which is a caller-programming error when they are empty
// rather than untrusted provider input.
//
// IT CONVERTS THE OWN-GRAPH HALF OF THE ENVELOPE ONLY. An edge naming a target
// graph is an edge into ANOTHER graph, so converting it here would write it into
// the collect's own graph — the native-cross-boundary edge the proxy convention
// exists to prevent. Those edges leave the envelope through CrossGraphEdges
// instead and are resolved by the collector-side pass. The split is here, at the
// conversion, rather than downstream: past this point the payload is
// []kgwire.BatchEdge, which carries no graph selector, so an edge that reaches
// the wire payload can no longer be told apart from an in-graph one.
//
// It does NOT sanitize node text: the UploadSink already runs sanitizeNodeText
// over every node before marshal (collector/remote/sink.go), so duplicating it
// here would be redundant work.
//
// Validation fails LOUD on a malformed envelope — an empty family, an empty
// instance, or a node with an empty type all return a non-nil error rather than
// silently shipping a degenerate result. A registered collector that returns
// garbage must surface as a collect error, never a silent no-op.
func (r *Result) ToCollectResult(graphType, graphName string) (*collectorwire.CollectResult, error) {
	if r == nil {
		return nil, fmt.Errorf("externalcollector: nil Result")
	}
	if graphType == "" {
		return nil, fmt.Errorf("externalcollector: graph family (the registration name) is empty")
	}
	if graphName == "" {
		return nil, fmt.Errorf("externalcollector: graph instance (the collect id) is empty")
	}

	nodes := make([]*knowledgev1.Node, 0, len(r.Nodes))
	for i := range r.Nodes {
		n := &r.Nodes[i]
		if n.Type == "" {
			return nil, fmt.Errorf("externalcollector: node[%d] (id=%q) has an empty type", i, n.ID)
		}
		meta := n.Metadata
		if strings.TrimSpace(n.Summary) != "" {
			// PROVENANCE IS STAMPED HERE BECAUSE IT CANNOT BE RECOVERED LATER, and
			// that is a fact about the store rather than a preference. A node written
			// with no summary has one DERIVED from its description on the way in
			// (store.AutoSummary), so by the time anything downstream looks, "the
			// collector wrote this summary" and "the store copied the description into
			// it" are the same bytes. Only this site, holding the collector's own
			// output, can still tell them apart.
			//
			// WHAT READS IT: the server's summary-gap eligibility, which never sends a
			// collector-summarized node to the LLM summarizer. Without the stamp that
			// rule would either miss every provided summary or, worse, treat every
			// derived one as provided and silently stop summarizing whole families.
			//
			// IT IS A METADATA KEY, DUPLICATED BY VALUE ACROSS THE MODULE BOUNDARY,
			// exactly as the LLM failure-marker keys already are: the two binaries are
			// separate Go modules and a hand-written shared package is forbidden, so
			// both ends spell the same literal. It moves no proto message — metadata
			// is an open map that already crosses.
			meta = withCollectorSummaryProvenance(meta)
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
			Metadata:    meta,
		})
	}

	edges := make([]kgwire.BatchEdge, 0, len(r.Edges))
	for i := range r.Edges {
		e := &r.Edges[i]
		if e.SourceGraph != "" || e.TargetGraph != "" {
			// Cross-graph on EITHER endpoint: CrossGraphEdges carries it and the
			// pass resolves it. This condition is the exact complement of that
			// lift's; the two are widened together or an edge lands in both
			// halves, which is a dangling in-graph edge to a foreign id.
			continue
		}
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
		GraphType: kgtypes.GraphType(graphType),
		GraphName: graphName,
		Nodes:     nodes,
		Edges:     edges,
		// The provider's completeness assertion rides through to the wire's
		// walk_complete, which is the server's deletion guard. The zero value is
		// the safe one: a result asserting false disables the deletion phase.
		WalkComplete: r.WalkComplete,
	}, nil
}
