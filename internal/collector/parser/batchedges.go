// SPDX-License-Identifier: Apache-2.0

package parser

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// ToBatchEdges converts resolved graph edges into the carrier the collector's
// wire-send chain consumes, prefixing both endpoints with prefix (the empty
// string on the local-collect path, "<repoName>/" for an external graph).
//
// IT COPIES Confidence, Method AND Evidence, and that is the point of the
// function existing. Two byte-identical conversion loops used to live at the
// two call sites, and neither copied any of the three: a multi-bind group would
// have been built by resolution and then arrived at the server at confidence 0,
// with no method and — decisively — with no group key, so the group would be
// gone by the time anything could read it while every other gate stayed green.
// Evidence is the group key; a converter that copies two of the three fields
// silently dissolves the groups it appears to carry.
//
// The three fields have a proven carrier the rest of the way: kgwire.BatchEdge
// holds all three, the wire BatchEdge message carries them, and the server
// copies Evidence onto the persisted edge.
func ToBatchEdges(edges []*knowledgev1.Edge, prefix string) []kgwire.BatchEdge {
	out := make([]kgwire.BatchEdge, len(edges))
	for i, e := range edges {
		out[i] = kgwire.BatchEdge{
			FromIdx:    -1,
			ToIdx:      -1,
			FromID:     prefix + e.FromId,
			ToID:       prefix + e.ToId,
			Type:       kgtypes.EdgeType(e.Type),
			Weight:     e.Weight,
			Confidence: e.Confidence,
			Method:     e.Method,
			Evidence:   e.Evidence,
		}
	}
	return out
}
