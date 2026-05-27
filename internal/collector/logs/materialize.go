// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"fmt"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// MaterializeLogGraph is a pure transform that converts a client-produced
// logs.CollectResult (the templates / streams / chunks / correlations /
// resolutions slices) into the canonical ([]*knowledgev1.Node, []kgwire.BatchEdge)
// shape consumed by the client's PersistBatch wire path.
//
// queryID is preserved on the caller's side via the graph name; it is not
// embedded in the returned slices. The caller is responsible for routing
// the result to the correct log graph (kgtypes.GraphLogs, name=queryID).
//
// Layout of the returned slices:
//
//   - Nodes:
//
//   - one NodeLogTemplate per template
//
//   - one NodeLogStream per stream
//
//   - one NodeLogChunk per chunk
//
//   - one NodeLogLabel per unique (key,value) low-card label
//
//   - one NodeProxy per ResolvedProxyEntry (deterministic ID derived
//     from BuildCrossGraphProxy with GraphCloud)
//
//   - Edges (all BatchEdges with FromIdx/ToIdx==-1, referenced by ID):
//
//   - has-label edges between streams and label nodes
//
//   - belongs-to / contains edges between chunks and streams/templates
//
//   - one EMITTED_BY edge per ResolvedProxyEntry (label → cloud proxy)
//
//   - one CORRELATES_WITH edge per StructurallyConfirmed correlation
//     (Confidence carries the cooccurrence score; method/evidence
//     describe the correlation source)
func MaterializeLogGraph(
	queryID string,
	templates []*wirelogs.LogTemplate,
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
	correlations []wirelogs.CorrelationResult,
	resolutions []wirelogs.ResolvedProxyEntry,
) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	if queryID == "" {
		return nil, nil, fmt.Errorf("logs: MaterializeLogGraph: empty queryID")
	}

	nodes, edges := AssembleGraphBatch(templates, streams, chunks)

	proxyNodes, proxyEdges, err := materializeProxies(resolutions)
	if err != nil {
		return nil, nil, fmt.Errorf("logs: MaterializeLogGraph: %w", err)
	}
	nodes = append(nodes, proxyNodes...)
	edges = append(edges, proxyEdges...)

	edges = append(edges, materializeCorrelations(correlations)...)

	return nodes, edges, nil
}

// materializeProxies builds one NodeProxy per ResolvedProxyEntry via the
// pure crossgraph.BuildCrossGraphProxy helper, plus an EMITTED_BY edge from each
// resolution's label node to its proxy.
func materializeProxies(resolutions []wirelogs.ResolvedProxyEntry) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	if len(resolutions) == 0 {
		return nil, nil, nil
	}
	nodes := make([]*knowledgev1.Node, 0, len(resolutions))
	edges := make([]kgwire.BatchEdge, 0, len(resolutions))
	seen := make(map[string]struct{}, len(resolutions))
	for _, r := range resolutions {
		// crossgraph.BuildCrossGraphProxy is the SHARED deterministic-ID proxy
		// builder both client and server use (byte-identical proxy IDs). It is
		// typed on the proto carrier directly and returns the owned
		// *knowledgev1.Node product. We feed it a SOURCE built by field
		// assignment via new() (no struct-literal copy of the noCopy proto) and
		// hand it a pointer to a fresh-literal proto ProxyTarget (the builder
		// takes *ProxyTarget so the embedded MessageState lock is never copied
		// by value).
		source := new(knowledgev1.Node)
		source.Type = string(kgtypes.NodeLogLabel)
		source.SymbolName = r.LabelKey + "=" + r.LabelValue
		source.Description = fmt.Sprintf(
			"cloud proxy for log label %s=%s (resolved to %s/%s)",
			r.LabelKey, r.LabelValue, r.Account, r.ResourceID,
		)
		proxy, err := crossgraph.BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
			GraphType: string(kgtypes.GraphCloud),
			Name:      r.Account,
			NodeId:    r.ResourceID,
		}, source)
		if err != nil {
			return nil, nil, fmt.Errorf("materializeProxies: build proxy %s/%s: %w", r.Account, r.ResourceID, err)
		}
		if _, dup := seen[proxy.Id]; !dup {
			nodes = append(nodes, proxy)
			seen[proxy.Id] = struct{}{}
		}
		edges = append(edges, BatchEdgeByID(LabelNodeID(r.LabelKey, r.LabelValue), proxy.Id, kgtypes.EdgeEmittedBy))
	}
	return nodes, edges, nil
}

// materializeCorrelations emits one CORRELATES_WITH BatchEdge per
// StructurallyConfirmed correlation. Confidence carries the cooccurrence
// score; Method and Evidence mirror the legacy writeCorrelations payload so
// consumers see the same audit string.
func materializeCorrelations(correlations []wirelogs.CorrelationResult) []kgwire.BatchEdge {
	if len(correlations) == 0 {
		return nil
	}
	edges := make([]kgwire.BatchEdge, 0, len(correlations))
	for _, c := range correlations {
		if !c.StructurallyConfirmed {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{
			FromIdx:    -1,
			ToIdx:      -1,
			FromID:     c.TemplateA,
			ToID:       c.TemplateB,
			Type:       kgtypes.EdgeCorrelatesWith,
			Confidence: c.CooccurrenceScore,
			Method:     "temporal+cloud-dependency",
			Evidence: fmt.Sprintf(
				"services=%s,%s resources=%s,%s score=%s",
				c.ServiceA, c.ServiceB, c.ResourceA, c.ResourceB,
				strconv.FormatFloat(c.CooccurrenceScore, 'f', 3, 64),
			),
		})
	}
	return edges
}
