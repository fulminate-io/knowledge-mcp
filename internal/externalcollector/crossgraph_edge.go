// SPDX-License-Identifier: Apache-2.0

package externalcollector

// crossgraph_edge.go — the CROSS-GRAPH half of a provider result: the edges
// whose Edge.SourceGraph or Edge.TargetGraph names another graph family, lifted
// out of the envelope so the collect path can resolve them instead of shipping
// them.
//
// THIS PACKAGE RESOLVES NOTHING. It decodes an envelope and converts it; the
// resolution is wire work (enumerate the named family, locate the endpoint,
// materialize a proxy, link into linkage) and belongs to the collect path that
// holds a graph caller. What lives here is the PARTITION and its carrier, which
// is the one thing only this package can do — past ToCollectResult the payload
// is []kgwire.BatchEdge and the target graph is gone.

// CrossGraphEdge is one contract edge that named a target graph. It is the
// plain-Go carrier between the envelope and the collector-side resolution pass,
// and deliberately NOT kgwire.BatchEdge: that type crosses the wire and carries
// no graph selector, so a cross-graph edge expressed as one would be
// indistinguishable from an in-graph edge the moment it was built.
//
// EXACTLY ONE OF THE TWO FAMILY FIELDS IS SET on an edge that reaches here, and
// it decides WHICH ENDPOINT IS FOREIGN. TargetGraph is the family ToID lives in,
// so FromID is a node of the collect's OWN graph; SourceGraph is the family
// FromID lives in, so ToID is the own-graph node. In both cases the family's
// INSTANCE is derived by enumerating that family, never asserted by the
// provider. An edge naming both is refused by the collect pass before it is
// resolved, so it never reaches this carrier as a resolvable edge.
type CrossGraphEdge struct {
	SourceGraph string
	TargetGraph string
	FromID      string
	ToID        string
	Type        string
	Weight      float64
	Confidence  float64
	Method      string
	Evidence    string
}

// CrossGraphEdges returns the envelope's edges that name a graph family on
// EITHER endpoint, in envelope order. It is the exact complement of what
// ToCollectResult converts: every edge of the envelope is in one of the two and
// never in both, so nothing a provider emitted is dropped by the split.
//
// THE CONDITION HERE AND THE ONE IN ToCollectResult MOVE TOGETHER. Widen one
// without the other and an edge is in BOTH halves — resolved into linkage AND
// written into the collect's own graph as a dangling edge to a foreign id — or
// in NEITHER, and silently lost. TestToCollectResult_PartitionsTheSourceGraphEdgesOut
// is the row that fails when they disagree.
//
// A nil Result and a result with no such edges both return nil, which is the
// shape the pass reads as "nothing to resolve".
func (r *Result) CrossGraphEdges() []CrossGraphEdge {
	if r == nil {
		return nil
	}
	var out []CrossGraphEdge
	for i := range r.Edges {
		e := &r.Edges[i]
		if e.SourceGraph == "" && e.TargetGraph == "" {
			continue
		}
		out = append(out, CrossGraphEdge{
			SourceGraph: e.SourceGraph,
			TargetGraph: e.TargetGraph,
			FromID:      e.FromID,
			ToID:        e.ToID,
			Type:        e.Type,
			Weight:      e.Weight,
			Confidence:  e.Confidence,
			Method:      e.Method,
			Evidence:    e.Evidence,
		})
	}
	return out
}
