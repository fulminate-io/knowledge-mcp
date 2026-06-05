// SPDX-License-Identifier: Apache-2.0

package render

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// edgesToProtoForTest mirrors the server's edgesToProto so fakes populate the
// typed ExecuteResponse.edges carrier (edges_json was migrated → repeated
// Edge). LastValidated rides as int64 unix-nanos (zero time → 0).
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
