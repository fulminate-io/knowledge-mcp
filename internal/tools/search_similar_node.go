// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// composeSimilarNodeSearch is the search mode:"similar" composer: given a node id,
// it resolves that node's STORED binary vector from the client-local HNSW segments
// and returns the node's nearest corpus neighbors, with the node itself excluded.
//
// It is the near-identical sibling of composeKnowledgeSearch — same
// Search→hydrate→render spine — differing only in (a) the query vector SOURCE (the
// node's stored vector resolved by id, NOT a freshly client-embedded query text)
// and (b) self-exclusion. Body, in order:
//
//  1. Resolve the stored vector via res.VectorByID. A resolve error → errorResult;
//     a not-found (the node is not embedded / not in any shipped segment yet) → a
//     LOUD errorResult naming the id and guiding the caller to let the client
//     pipeline finish embedding and the server ship a refreshed segment. NEVER a
//     silent empty success — that is the failure mode the ticket forbids.
//     (manage(rebuild_segments) is deliberately NOT suggested: it supports only
//     code and registered custom graph types, never the builtin knowledge graph.)
//  2. Search the CLIENT engine with an EMPTY query text + the resolved vector and
//     k+1: empty text runs the HNSW arm over the STORED-vector space with no fresh
//     embed; k+1 leaves room to drop the self hit and still return k neighbors.
//  3. Drop the self hit (ID == nodeID), then truncate to k.
//  4. Hydrate the ranked ids in ONE bulk RETURN_MODE_NODES read.
//  5. Render for the caller (default text when format is empty).
//
// NON-GOAL: no cluster-size annotation per neighbor — that would need a clusters
// read the search path does not have.
func composeSimilarNodeSearch(
	ctx context.Context,
	gc GraphCaller,
	mgr SegmentSearcher,
	res SegmentVectorResolver,
	nodeID string,
	k int,
	format string,
	fields []string,
) kgtools.ToolResult {
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}

	vec, ok, err := res.VectorByID(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, nodeID)
	if err != nil {
		return errorResult("similar search: resolve stored vector for " + nodeID + ": " + err.Error())
	}
	if !ok || len(vec) == 0 {
		return errorResult("similar search: node " + nodeID +
			" has no stored vector yet (not embedded into a shipped segment) — wait for the client pipeline to finish embedding and the server to ship a refreshed knowledge segment, then retry")
	}

	// Empty query text → HNSW-only over the STORED-vector space (no fresh embed);
	// k+1 leaves room for the self-exclusion below.
	hits, err := mgr.Search(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, "", vec, k+1)
	if err != nil {
		return errorResult("similar search: client engine: " + err.Error())
	}

	// Drop the self hit (the query node is its own exact-match nearest neighbor),
	// then truncate to k.
	filtered := hits[:0]
	for _, h := range hits {
		if h.ID == nodeID {
			continue
		}
		filtered = append(filtered, h)
	}
	if len(filtered) > k {
		filtered = filtered[:k]
	}

	results, err := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: string(kgtypes.GraphKnowledge)}, filtered)
	if err != nil {
		return errorResult("similar search: hydrate: " + err.Error())
	}

	if format == "" {
		format = "text"
	}
	return engine.RenderForCaller("", results, format, fields, "vector")
}
