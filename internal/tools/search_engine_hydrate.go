// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// hydrateSelector is the graph-routing envelope a client-engine search hydrates
// against: the same fields buildTarget consumes, so the bulk ids[] read lands on
// the right graph (knowledge default / code repo / practice language / ...).
type hydrateSelector struct {
	Graph    string
	Repo     string
	Account  string
	Name     string
	Language string
	Branch   string
}

// hydrateEngineHits turns a CLIENT-engine RRF result (ranked []searchengine.Hit
// carrying ID + fused score) into rank-ordered engine.SearchResult rows by
// hydrating the full nodes in ONE wire read.
//
// It issues exactly ONE gc.Execute — a RETURN_MODE_NODES bulk ids[] read over
// the WHOLE ranked ID list (engine.Compile("query", {ids,…}) lowers to
// QueryPlan.Ids → store.ByIDs; no N+1) — then builds a map[id]*Node from the
// response and walks the RANKED hits IN ORDER, looking each id up in the map.
// The join is by ID-MAP, never by response position: RETURN_MODE_NODES returns
// `repeated Node` with NO guaranteed correspondence to the input id order, so
// zipping response[i] to rankedIDs[i] would mis-pair rows. A ranked id missing
// from the map (tombstoned/deleted between rank and hydrate) is SKIPPED, exactly
// as fetchNodesByIDs treats a missing id as absent. Each emitted row carries the
// hit's FUSED score, not a re-derived one.
//
// Mirrors the established bulk-hydrate idiom (thought/wire.go:105 fetchNodesByIDs:
// marshal {ids} → Compile("query") → Execute → engine.DecodeNodes → out[n.Id]=n).
func hydrateEngineHits(
	ctx context.Context,
	gc GraphCaller,
	sel hydrateSelector,
	hits []searchengine.Hit,
) ([]engine.SearchResult, error) {
	if len(hits) == 0 || gc == nil {
		return nil, nil
	}

	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}

	args, err := json.Marshal(map[string]any{
		"ids":      ids,
		"graph":    sel.Graph,
		"repo":     sel.Repo,
		"account":  sel.Account,
		"name":     sel.Name,
		"language": sel.Language,
		"branch":   sel.Branch,
	})
	if err != nil {
		return nil, err
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		// Should not happen for a fixed ids[] read shape; fail soft like the
		// thought-wire hydrators rather than panic.
		slog.Warn("hydrateEngineHits: ids[] query not reducible")
		return nil, nil
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		byID[n.GetId()] = n
	}

	// Walk the RANKED hits in order; join by id-map; carry the fused score.
	results := make([]engine.SearchResult, 0, len(hits))
	for _, h := range hits {
		n, ok := byID[h.ID]
		if !ok {
			continue // tombstoned/deleted between rank and hydrate — skip.
		}
		results = append(results, engine.SearchResult{Node: n, Score: h.Score})
	}
	return results, nil
}
