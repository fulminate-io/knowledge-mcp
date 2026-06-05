// SPDX-License-Identifier: Apache-2.0

package engine

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// searchResultsToProtoForTest / traversalResultsToProtoForTest mirror the
// server's searchResultsToProto / traversalResultsToProto so this package's fakes
// seed the typed search_results / traversal_results carriers the real server now
// emits. P2-T5 deleted the node_json blob field: each carrier now holds
// the nested *knowledgev1.Node DIRECTLY (the value-embed wire), so these helpers
// take the client-side wrapper rows ([]SearchResult / []TraversalResult, whose
// Node is already *knowledgev1.Node) and copy them into the typed proto carriers
// with no marshal step — the encode twin of decodeSearch / decodeTraversal.
func searchResultsToProtoForTest(results []SearchResult) []*knowledgev1.HydratedResult {
	out := make([]*knowledgev1.HydratedResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.HydratedResult{Node: r.Node, Score: r.Score}
	}
	return out
}

func traversalResultsToProtoForTest(results []TraversalResult) []*knowledgev1.TraversalResult {
	out := make([]*knowledgev1.TraversalResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.TraversalResult{Node: r.Node, Distance: int32(r.Distance)}
	}
	return out
}

// edgesToProtoForTest mirrors the server's edgesToProto
// (cmd/knowledge-server/bootstrap/engine_carrier_convert.go) so this package's
// fakes seed the typed ExecuteResponse.edges carrier the real server now emits
// (the migration from edges_json → repeated Edge). It is the encode twin of this
// package's EdgesFromProto decoder, taking the proto-native []knowledgev1.Edge
// the tests build. LastValidated rides as int64 unix-nanos (zero time → 0).
func edgesToProtoForTest(edges []knowledgev1.Edge) []*knowledgev1.Edge {
	out := make([]*knowledgev1.Edge, len(edges))
	for i := range edges {
		e := &edges[i]
		out[i] = &knowledgev1.Edge{
			FromId:        e.FromId,
			ToId:          e.ToId,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: e.LastValidated,
		}
	}
	return out
}
