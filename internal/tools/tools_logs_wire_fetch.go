// SPDX-License-Identifier: Apache-2.0

// Package tools — wire-fetch helpers for log graph node bulk reads.
//
// BCN11.3 client architecture: the persistent log graph lives on the
// server. Client handlers can't reach for store.Store() anymore — the
// production stdio binary has no local DB initialized. Instead they
// bulk-fetch the templates / streams / chunks they need via the
// GraphCaller surface (deps.go), then assemble a *logState (the plain
// pre-fetched value type) and run formatters against that.
//
// This file owns the bulk-fetch primitives:
//
//   - fetchAllLogNodes — three raw type-browse Match-all Execute reads
//     (one per log node type, Limit:0 = no cap), decodes chunk content_b64
//     back to raw bytes. Built as raw QueryPlans (not via engine.Compile) so
//     the LLM-facing browse default-10 does not cap the internal full pull.
//
//   - fetchLogNodesByIDs — bulk hydrate-by-IDs (one query, no per-ID
//     loop). Caller passes the ID list; the server's IDs-projection
//     returns hydrated nodes in one round-trip.
//
// Three RPC ceiling (one per node type) for the full bulk fetch. No
// engine cache — every MCP call refetches per reviewer T2-B.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fetchAllLogNodes pulls every template/stream/chunk node for the given
// queryID from the server. Three RPCs (one per type) — the chunk RPC
// requests the content_b64 projection so binary Content survives the
// JSON wire path. Returned slices preserve server ordering.
//
// NOTE: label + proxy nodes are NOT returned by this helper. Use
// fetchAllLogAuxNodes to grab them too — they're referenced via edges
// (HAS_LABEL → LogLabel, EMITTED_BY → NodeProxy) but the formatter
// surfaces are distinct enough that the orchestrator merges them into
// logState.byID separately.
func fetchAllLogNodes(
	ctx context.Context,
	gc GraphCaller,
	queryID string,
) (templates, streams, chunks []*knowledgev1.Node, err error) {
	if gc == nil {
		return nil, nil, nil, fmt.Errorf("fetchAllLogNodes: gc is nil")
	}
	templates, err = fetchLogNodesByType(ctx, gc, queryID, string(kgtypes.NodeLogTemplate), false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("templates: %w", err)
	}
	streams, err = fetchLogNodesByType(ctx, gc, queryID, string(kgtypes.NodeLogStream), false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("streams: %w", err)
	}
	chunks, err = fetchLogNodesByType(ctx, gc, queryID, string(kgtypes.NodeLogChunk), true)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("chunks: %w", err)
	}
	return templates, streams, chunks, nil
}

// fetchAllLogAuxNodes pulls log-label + cloud-proxy nodes for the given
// queryID. Labels are LogLabel nodes (HAS_LABEL targets); proxies are
// NodeProxy nodes in the log graph (EMITTED_BY targets, written by the
// log pipeline's cloud-resolver). Two RPCs, no content_b64 — neither
// type carries binary data.
func fetchAllLogAuxNodes(
	ctx context.Context,
	gc GraphCaller,
	queryID string,
) (labels, proxies []*knowledgev1.Node, err error) {
	if gc == nil {
		return nil, nil, fmt.Errorf("fetchAllLogAuxNodes: gc is nil")
	}
	labels, err = fetchLogNodesByType(ctx, gc, queryID, string(kgtypes.NodeLogLabel), false)
	if err != nil {
		return nil, nil, fmt.Errorf("labels: %w", err)
	}
	proxies, err = fetchLogNodesByType(ctx, gc, queryID, string(kgtypes.NodeProxy), false)
	if err != nil {
		return nil, nil, fmt.Errorf("proxies: %w", err)
	}
	return labels, proxies, nil
}

// fetchLogNodesByType issues one type-browse RPC for the given log node
// type and returns ALL its nodes. When wantContent is true, requests
// the content_b64 projection and base64-decodes it back onto Node.Content
// — required for log chunks because their Content carries raw zstd bytes
// that don't survive plain JSON serialization.
//
// This is an INTERNAL Match-all aggregation: it wants EVERY node of the type
// for the in-client logState assembly, so the plan carries Limit:0 (no cap),
// which the server now honors per the proto contract (FUL-302). It builds the
// raw typed QueryPlan and calls Execute DIRECTLY — the sibling fetchAllLogEdges
// pattern — rather than routing through engine.Compile/the JSON query tool. The
// compile path applies the LLM-facing browse default-10 (applyBrowseLimitOffset),
// which would silently re-cap this internal full-type pull at 10; the raw plan
// bypasses that default so Limit:0 reaches the store as a genuine no-cap.
func fetchLogNodesByType(
	ctx context.Context,
	gc GraphCaller,
	queryID, nodeType string,
	wantContent bool,
) ([]*knowledgev1.Node, error) {
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, err
	}
	plan := &knowledgev1.QueryPlan{
		Selection:  &knowledgev1.Selection{NodeType: nodeType},
		ContentB64: wantContent,
		// Limit unset → 0 → no cap (server honors the proto contract; this is an
		// internal Match-all that wants every node of the type).
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: &knowledgev1.GraphSelector{Graph: "logs", Name: queryID},
	})
	if err != nil {
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return decodeLogBrowseResponse(resp, wantContent)
}

// fetchLogNodesByIDs hydrates a caller-supplied set of node IDs in ONE
// bulk query. Uses the server's IDs-projection (queryArgs.IDs) so there
// is no per-ID loop. Returns nodes in caller-supplied order (best-effort
// — duplicates and missing IDs follow the server's renderGenericNodesByIDs
// behavior).
//
// wantContent threads the content_b64 projection on or off so callers
// that need chunk bodies pay the base64 round-trip and callers that need
// only metadata don't.
func fetchLogNodesByIDs(
	ctx context.Context,
	gc GraphCaller,
	queryID string,
	ids []string,
	wantContent bool,
) ([]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, fmt.Errorf("fetchLogNodesByIDs: gc is nil")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	args, err := marshalLogIDsQueryArgs(queryID, ids, wantContent)
	if err != nil {
		return nil, err
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return decodeLogBrowseResponse(resp, wantContent)
}

// marshalLogIDsQueryArgs builds the JSON payload for a bulk hydrate-by-IDs
// query against the logs graph (Execute carrier seam). wantContent opts into the
// content_b64 carrier as in marshalLogTypeQueryArgs.
func marshalLogIDsQueryArgs(queryID string, ids []string, wantContent bool) (json.RawMessage, error) {
	payload := struct {
		Graph      string   `json:"graph"`
		Name       string   `json:"name"`
		IDs        []string `json:"ids"`
		ContentB64 bool     `json:"content_b64,omitempty"`
	}{
		Graph:      "logs",
		Name:       queryID,
		IDs:        ids,
		ContentB64: wantContent,
	}
	return json.Marshal(payload)
}

// decodeLogBrowseResponse decodes the nodes_json carrier into *knowledgev1.Node values.
// When wantContent is true the chunk-side Content rode the content_b64 carrier
// (raw zstd frame, binary-safe), so it decodes via engine.DecodeNodesContentB64
// (base64 → raw bytes onto Node.Content); otherwise plain engine.DecodeNodes.
func decodeLogBrowseResponse(resp *knowledgev1.ExecuteResponse, wantContent bool) ([]*knowledgev1.Node, error) {
	if wantContent {
		return engine.DecodeNodesContentB64(resp)
	}
	return engine.DecodeNodes(resp)
}
