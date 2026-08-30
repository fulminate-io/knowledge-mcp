// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/base64"
	"fmt"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// nanosToTime converts an int64 unix-nanos value (the value-embed proto
// timestamp representation) to a time.Time for the client
// renderers, mapping 0 → the zero time.Time. Shared by every render_*.go site
// that formats a knowledgev1.Edge timestamp.
func nanosToTime(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// engine_decode.go decodes the engine's ExecuteResponse TYPED carriers into the
// client-side node types, symmetric to the server's resultToResponse encode
// (cmd/knowledge-server/bootstrap/engine_encode.go). The server populates the
// typed Nodes / search_results.node / traversal_results.node fields; these
// decoders read them DIRECTLY (T5 deleted the nodes_json/node_json blob
// boundary — the read is now field access, no json.Unmarshal). The server
// populates exactly one sub-list per return mode, so an absent carrier is the
// NORMAL case for the other modes — every decoder returns an empty slice (NOT an
// error) on an empty/nil carrier.
//
// This is intentionally NOT hydrateFromJSON (search_rerank.go): that decoder
// reads the legacy store.SearchJSONResponse ENVELOPE, a different wire shape
// than the engine's typed HydratedResult carrier.

// SearchResult is the client-side search hit (node + score), mirroring the
// former store.HydratedResult's field set — since removed from the server store
// package along with the rest of the retired search carriers, so there is no
// line left to cite (cmd/knowledge-server/internal/store/search_types.go records
// the removal) — with
// the embedded node retyped to the wire proto *knowledgev1.Node — T5
// drops the store.Node wrapper layer from the client read path. Method-free DTO.
//
// Graph + GraphInstance carry the result's SOURCE-GRAPH identity: the
// graph family ("code"/"cloud"/"cicd"/"practice"/"knowledge"/"logs") and the
// per-result instance (repo / account / language / log queryID; empty for the
// knowledge default). Every json-emitting compose path stamps them at hydrate/
// merge time from the selector it already has, so a json consumer (the graph-UI)
// can traverse each result in its own graph. The practice fan-out is the case
// that varies PER HIT, so the stamp is per-result, not per-call.
type SearchResult struct {
	Node          *knowledgev1.Node
	Score         float64
	Graph         string
	GraphInstance string
}

// TraversalResult is the client-side traversal hit (node + distance), mirroring
// store.TraversalResult's field set
// (cmd/knowledge-server/internal/store/graph_types.go:363)
// with the embedded node retyped to *knowledgev1.Node. Method-free DTO.
type TraversalResult struct {
	Node     *knowledgev1.Node
	Distance int
}

// decodeNodes decodes the typed ExecuteResponse.Nodes carrier (RETURN_MODE_NODES)
// into []*knowledgev1.Node. Empty carrier → nil slice. The server populates this
// from &node.Node (value-embed), so the typed nodes ARE the
// wire protos — no decode step, just the field read.
func decodeNodes(resp *knowledgev1.ExecuteResponse) ([]*knowledgev1.Node, error) {
	return resp.GetNodes(), nil
}

// decodeSearch decodes the typed ExecuteResponse.search_results carrier
// (RETURN_MODE_SEARCH) into []SearchResult. Each element reads the typed
// p.GetNode() (*knowledgev1.Node) and carries Score as the typed field. Empty
// carrier → nil.
func decodeSearch(resp *knowledgev1.ExecuteResponse) ([]SearchResult, error) {
	protos := resp.GetSearchResults()
	if len(protos) == 0 {
		return nil, nil
	}
	results := make([]SearchResult, len(protos))
	for i, p := range protos {
		results[i] = SearchResult{Node: p.GetNode(), Score: p.GetScore()}
	}
	return results, nil
}

// decodeTraversal decodes the typed ExecuteResponse.traversal_results carrier
// (RETURN_MODE_TRAVERSAL) into []TraversalResult. Each element reads the typed
// p.GetNode() (*knowledgev1.Node) and carries Distance as the typed field. Empty
// carrier → nil.
func decodeTraversal(resp *knowledgev1.ExecuteResponse) ([]TraversalResult, error) {
	protos := resp.GetTraversalResults()
	if len(protos) == 0 {
		return nil, nil
	}
	results := make([]TraversalResult, len(protos))
	for i, p := range protos {
		results[i] = TraversalResult{Node: p.GetNode(), Distance: int(p.GetDistance())}
	}
	return results, nil
}

// decodeTraversalEdges decodes ExecuteResponse.traversal_edges (the
// include_edge_metadata carrier) into []knowledgev1.Edge. The server populates it
// only when include_edge_metadata was set; empty carrier → nil slice (the NORMAL
// case for a plain traversal). Infallible — the typed proto decode cannot fail.
func decodeTraversalEdges(resp *knowledgev1.ExecuteResponse) []knowledgev1.Edge {
	return EdgesFromProto(resp.GetTraversalEdges())
}

// EdgesFromProto converts the typed proto Edge carrier into []knowledgev1.Edge —
// a values-from-pointers field copy (LastValidated stays int64 unix-nanos, the
// proto representation). Shared across every client edge-carrier decode
// (RETURN_MODE_EDGES, traversal-edge-metadata, cloud-subgraph, examine). Empty
// carrier → nil.
func EdgesFromProto(in []*knowledgev1.Edge) []knowledgev1.Edge {
	if len(in) == 0 {
		return nil
	}
	out := make([]knowledgev1.Edge, len(in))
	for i, e := range in {
		out[i] = knowledgev1.Edge{
			FromId:        e.GetFromId(),
			ToId:          e.GetToId(),
			Type:          e.GetType(),
			Weight:        e.GetWeight(),
			Confidence:    e.GetConfidence(),
			Method:        e.GetMethod(),
			Evidence:      e.GetEvidence(),
			LastValidated: e.GetLastValidated(),
		}
	}
	return out
}

// DecodeNodes / DecodeEdges / DecodeTraversal are the exported wrappers the
// cmd/knowledge/internal/tools per-graph composers use to decode the typed
// ExecuteResponse carriers (RETURN_MODE_NODES / RETURN_MODE_EDGES /
// RETURN_MODE_TRAVERSAL). They delegate to the package-internal decoders so the
// empty-carrier guard + carrier-name error wrapping stay single-sourced.
func DecodeNodes(resp *knowledgev1.ExecuteResponse) ([]*knowledgev1.Node, error) {
	return decodeNodes(resp)
}

// DecodeNodesContentB64 is the inverse of the server's content_b64 encode
// (engine_encode.go encodeNodeContentB64): it decodeNodes then base64-decodes
// each non-empty Node.Content back into raw bytes. Use it ONLY when the QueryPlan
// carried content_b64=true (the log-chunk fetch); the plain DecodeNodes stays
// byte-transparent (a base64 string is a valid Content string) so the 30+ existing
// DecodeNodes consumers are UNAFFECTED. The decode logic mirrors
// decodeLogBrowseResponse's wantContent arm (tools_logs_wire_fetch.go:259-265):
// base64.StdEncoding over the Content string, skipping empties.
func DecodeNodesContentB64(resp *knowledgev1.ExecuteResponse) ([]*knowledgev1.Node, error) {
	nodes, err := decodeNodes(resp)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if nodes[i].Content == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(nodes[i].Content)
		if derr != nil {
			return nil, fmt.Errorf("DecodeNodesContentB64: decode content_b64 for %q: %w", nodes[i].Id, derr)
		}
		nodes[i].Content = string(decoded)
	}
	return nodes, nil
}

// DecodeEdges decodes the RETURN_MODE_EDGES carrier into raw []knowledgev1.Edge.
func DecodeEdges(resp *knowledgev1.ExecuteResponse) ([]knowledgev1.Edge, error) {
	return decodeEdgesRaw(resp)
}

// DecodeTraversal decodes the RETURN_MODE_TRAVERSAL carrier into
// []TraversalResult.
func DecodeTraversal(resp *knowledgev1.ExecuteResponse) ([]TraversalResult, error) {
	return decodeTraversal(resp)
}

// DecodeSearch decodes the RETURN_MODE_SEARCH carrier into []SearchResult.
func DecodeSearch(resp *knowledgev1.ExecuteResponse) ([]SearchResult, error) {
	return decodeSearch(resp)
}

// DecodeGraphNames decodes ExecuteResponse.graph_names (the
// RETURN_MODE_GRAPH_NAMES carrier) into []*knowledgev1.GraphInfo — the graph
// CATALOG the engine enumerates for the target GraphType. The carrier is already
// the typed proto repeated field, so this returns it directly (no value copy —
// the proto message embeds a sync mutex, so a []GraphInfo value slice would trip
// copylocks). Empty carrier → nil slice, nil error. Consumers read gi.Name (the
// pointer-field access) to project the catalog.
func DecodeGraphNames(resp *knowledgev1.ExecuteResponse) ([]*knowledgev1.GraphInfo, error) {
	return resp.GetGraphNames(), nil
}

// decodeEdgesRaw decodes ExecuteResponse.edges (the RETURN_MODE_EDGES carrier)
// into raw []knowledgev1.Edge — the full Weight/Confidence/Method/Evidence/
// LastValidated edges the engine collects via db.IterEdges for a by_id / ids[] /
// from_id pivot set. The by-id consumer uses this to compose the
// query(id, include_edges) edge summary client-side (dispatch_byid.go). Empty
// carrier → nil (the node has no edges).
func decodeEdgesRaw(resp *knowledgev1.ExecuteResponse) ([]knowledgev1.Edge, error) {
	return EdgesFromProto(resp.GetEdges()), nil
}
