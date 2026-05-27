// SPDX-License-Identifier: Apache-2.0

// Package enginetest holds shared typed test-fixture builders for the
// knowledgev1 wire carriers consumed by the client engine decode layer.
//
// After P2-T5 deletes the ExecuteResponse.nodes_json / HydratedResult.node_json
// / TraversalResult.node_json blob fields, the ~46 client test files that built
// &knowledgev1.ExecuteResponse{NodesJson: json.Marshal([]*store.Node{...})} (and
// the wrapper carriers via per-package local helpers) need a single typed target
// that populates ONLY the typed fields (Nodes / SearchResults / TraversalResults
// and the nested Node / Score / Distance). These builders are that target.
//
// This is a NON-_test.go file in a leaf package so it is importable across the
// client test packages; it imports ONLY gen/knowledge/v1. The builder bodies
// mirror the server dual-encode shape (cmd/knowledge-server/bootstrap/
// engine_encode.go:66-68 for the Nodes loop; engine_carrier_convert.go:138-142
// / :159-163 for the HydratedResult / TraversalResult shape) MINUS the
// json.Marshal the field deletion removes.
package enginetest

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// ResponseWithNodes builds an ExecuteResponse carrying the given typed nodes in
// the Nodes field — the NodesJson:-blob replacement target for node-list
// fixtures. Sets only Nodes; never the deleted nodes_json blob.
func ResponseWithNodes(nodes ...*knowledgev1.Node) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{Nodes: nodes}
}

// ResponseWithNode is the single-node sibling of ResponseWithNodes for the
// common one-node fixture.
func ResponseWithNode(n *knowledgev1.Node) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}
}

// SearchResponseWith builds an ExecuteResponse carrying the given typed search
// results in the SearchResults field — the search-wrapper fixture target. Each
// HydratedResult sets only Node + Score; never the deleted node_json blob.
func SearchResponseWith(results ...*knowledgev1.HydratedResult) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{SearchResults: results}
}

// TraversalResponseWith builds an ExecuteResponse carrying the given typed
// traversal results in the TraversalResults field — the traversal-wrapper
// fixture target. Each TraversalResult sets only Node + Distance; never the
// deleted node_json blob.
func TraversalResponseWith(results ...*knowledgev1.TraversalResult) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{TraversalResults: results}
}
