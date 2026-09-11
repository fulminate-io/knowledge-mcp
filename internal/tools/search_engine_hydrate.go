// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// hydrateSelector is the graph-routing envelope a client-engine search hydrates
// against: the same fields buildTarget consumes, so the bulk ids[] read lands on
// the right graph (knowledge default / code repo / a named custom graph / ...).
//
// THERE IS NEITHER AN Account NOR A Language FIELD, and each removal closed a
// measured defect rather than trimming an unused member. Account sat in the
// instance-precedence chain below AHEAD of Name, and exactly one composer ever
// set it — pivotHydrateSelector, which copied the caller's `account` tool param
// straight in. Since the account-keyed families retired, that param is accepted
// and ignored by owner ruling, so the only thing the field could still do was
// stamp a caller-supplied string as the GraphInstance of rows that came from
// somewhere else. Language was practice's instance field until the family became
// one combined graph and the selector was refused on every arm; the same
// composer was its last setter, and a value it stamped would have named a graph
// the hydrate read is refused for.
type hydrateSelector struct {
	Graph  string
	Repo   string
	Name   string
	Branch string
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
		"ids":    ids,
		"graph":  sel.Graph,
		"repo":   sel.Repo,
		"name":   sel.Name,
		"branch": sel.Branch,
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

	// The source-graph identity is the SAME for every hit in this
	// hydrate call — they were all ranked against ONE selector — so it is derived
	// once from the selector and stamped on each row. graph + instance feed the
	// graph-UI's per-result traverse. Covers knowledge-search /
	// practice-single / practice-fanout (one hydrate call PER language, so each
	// call's instance is that language) / registered / similar — every funnel that
	// reaches hydrateEngineHits.
	graph := sel.Graph
	if graph == "" {
		graph = string(kgtypes.GraphKnowledge) // the engine treats "" as knowledge.
	}
	instance := hydrateSelectorInstance(sel)

	// Walk the RANKED hits in order; join by id-map; carry the fused score.
	results := make([]engine.SearchResult, 0, len(hits))
	for _, h := range hits {
		n, ok := byID[h.ID]
		if !ok {
			continue // tombstoned/deleted between rank and hydrate — skip.
		}
		results = append(results, engine.SearchResult{
			Node:          n,
			Score:         h.Score,
			Graph:         graph,
			GraphInstance: instance,
		})
	}
	return results, nil
}

// hydrateSelectorInstance picks the per-result instance string from the
// hydrateSelector: the field a buildTarget consumes for this graph family — Repo
// for code, Name for a registered custom family. A SINGLETON family has no
// instance and reads empty, which is what the knowledge default, checks and
// practice all return. When both are set (defensive — the composers set exactly
// one) the code→name precedence mirrors the selector-routing order.
//
// TWO ARMS SAT HERE AND WENT WITH THEIR FIELDS. An Account arm sat between Repo
// and Name and out-ranked Name, so a read that legitimately named its instance
// had that name overwritten by an `account` the product no longer keys anything
// on; a Language arm sat after Name for the practice family, which addresses no
// instance since its per-language graphs became one.
func hydrateSelectorInstance(sel hydrateSelector) string {
	switch {
	case sel.Repo != "":
		return sel.Repo
	case sel.Name != "":
		return sel.Name
	default:
		return ""
	}
}
