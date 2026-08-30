// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
//     a not-found → absentSeedVectorMessage (below) reads the seed node and the
//     message names the actual cause: still embedding, deleted, or absent. NEVER a
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
		return errorResult(absentSeedVectorMessage(ctx, gc, nodeID))
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

// absentSeedVectorMessage builds the error text for the branch where the seed
// node resolves NO stored vector. Three DIFFERENT states land here and they call
// for three different things from the caller, so the message names which one it
// actually is rather than assuming the most common:
//
//   - live but not embedded → the vector genuinely is still coming; the original
//     wait-for-the-pipeline guidance is correct and is kept verbatim;
//   - deleted → the vector went with the node (SegmentedIndex.VectorByID is
//     liveness-aware, so a tombstoned member is declined). Telling this caller to
//     wait would promise an arrival that never comes;
//   - absent → there is no such node to be near anything.
//
// COST: one extra ids[] read, issued ONLY on this error branch — a successful
// similar search never pays for it. The composer has no prior knowledge of the
// seed node to reuse: res.VectorByID is the first thing it does, and the only
// other node read on the path (hydrateEngineHits) carries the NEIGHBOR ids and
// runs after the search.
//
// The read carries include_tombstones deliberately: without it the server's
// executeByIDs drops a tombstoned row, and a deleted node would be
// indistinguishable from one that never existed. With it, a returned row whose
// TombstonedAt is non-zero IS the deletion proof, and an empty result means
// absent for real rather than hidden-by-the-default-filter.
//
// A probe that FAILS is reported as a failed probe. It is not defaulted into any
// of the three verdicts: each of those is a claim about the corpus, and a read
// that did not return cannot back one.
func absentSeedVectorMessage(ctx context.Context, gc GraphCaller, nodeID string) string {
	node, found, err := readSeedNode(ctx, gc, nodeID)
	switch {
	case err != nil:
		return "similar search: node " + nodeID +
			" has no stored vector, and the read that would say whether it is live-but-unembedded, deleted, or absent failed: " + err.Error()
	case !found:
		return "similar search: no such node " + nodeID +
			" in the knowledge graph (read including tombstones, so this is absence, not deletion) — name an existing node"
	case node.GetTombstonedAt() != 0:
		return "similar search: node " + nodeID +
			" was deleted — its stored vector is gone with it, so similar-mode cannot seed from it; name a live node"
	default:
		return "similar search: node " + nodeID +
			" has no stored vector yet (not embedded into a shipped segment) — wait for the client pipeline to finish embedding and the server to ship a refreshed knowledge segment, then retry"
	}
}

// readSeedNode fetches ONE node by id, tombstones included, and reports
// (node, found, error). It is the ids[] shape hydrateEngineHits uses — the
// bulk-hydrate carrier that lowers to the server's executeByIDs — with a
// single-element id list, so the seed probe and the neighbor hydrate travel the
// same well-trodden read path.
func readSeedNode(ctx context.Context, gc GraphCaller, nodeID string) (*knowledgev1.Node, bool, error) {
	args, err := json.Marshal(map[string]any{
		"ids":                []string{nodeID},
		"graph":              string(kgtypes.GraphKnowledge),
		"include_tombstones": true,
	})
	if err != nil {
		return nil, false, err
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		return nil, false, errors.New("ids[] query not reducible")
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, false, err
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, false, err
	}
	for _, n := range nodes {
		if n.GetId() == nodeID {
			return n, true, nil
		}
	}
	return nil, false, nil
}
