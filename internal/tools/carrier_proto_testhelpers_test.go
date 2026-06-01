// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// nodePtrs converts a value slice of wire nodes into the pointer slice the
// enginetest.ResponseWithNodes builder takes (the carriers hold *knowledgev1.Node
// — knowledgev1.Node carries a noCopy, so value-slice fakes address each element).
func nodePtrs(nodes []knowledgev1.Node) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, len(nodes))
	for i := range nodes {
		out[i] = &nodes[i]
	}
	return out
}

// searchResultsToProtoForTest / traversalResultsToProtoForTest mirror the
// server's searchResultsToProto / traversalResultsToProto: each sets the TYPED
// node carrier (T5/FUL-295 deleted the node_json blob field — the node IS the
// wire proto now), carrying score/distance as the typed fields.
func searchResultsToProtoForTest(results []engine.SearchResult) []*knowledgev1.HydratedResult {
	out := make([]*knowledgev1.HydratedResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.HydratedResult{Node: r.Node, Score: r.Score}
	}
	return out
}

func traversalResultsToProtoForTest(results []engine.TraversalResult) []*knowledgev1.TraversalResult {
	out := make([]*knowledgev1.TraversalResult, len(results))
	for i, r := range results {
		out[i] = &knowledgev1.TraversalResult{Node: r.Node, Distance: int32(r.Distance)}
	}
	return out
}

// carrier_proto_testhelpers_test.go provides the proto converters the
// server-simulating fakes in this package use to populate the typed proto
// carriers FUL-276 migrated off the bytes *_json convention. After T5.5 the
// fakes hold the proto carriers directly (GraphStats / GraphInfo / Edge /
// OverrideConfig), so the former store→proto converters are gone; only the
// engine-result wrappers + the proto→kgwire batch-edge decode remain.

// batchEdgesFromProtoForTest is the server-decode inverse — used by fakes that
// simulate the server's decode of a collect request's edges carrier back into
// the client build-carrier []kgwire.BatchEdge.
func batchEdgesFromProtoForTest(in []*knowledgev1.BatchEdge) []kgwire.BatchEdge {
	out := make([]kgwire.BatchEdge, len(in))
	for i, e := range in {
		var lastValidated time.Time
		if n := e.GetLastValidated(); n != 0 {
			lastValidated = time.Unix(0, n)
		}
		out[i] = kgwire.BatchEdge{
			FromIdx:       int(e.GetFromIdx()),
			ToIdx:         int(e.GetToIdx()),
			FromID:        e.GetFromId(),
			ToID:          e.GetToId(),
			Type:          kgtypes.EdgeType(e.GetType()),
			Weight:        e.GetWeight(),
			Confidence:    e.GetConfidence(),
			Method:        e.GetMethod(),
			Evidence:      e.GetEvidence(),
			LastValidated: lastValidated,
		}
	}
	return out
}
