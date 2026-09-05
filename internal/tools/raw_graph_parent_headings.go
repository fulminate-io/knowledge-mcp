// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// raw_graph_parent_headings.go resolves a raw-graph hit's LOCALITY: the heading
// of the section that contains it. That context line ("under: <heading> | p. N |
// #anchor") is what makes a paragraph hit actionable at all — a web paragraph
// carries no SymbolName and no Description, so without the heading a hit renders
// as a bare hex id.
//
// THIS FILE USED TO HOLD A WHOLE-GRAPH DRAIN-AND-RANK, and that path is gone
// rather than parked beside the current one. Raw web and pdf graphs are now
// enrolled EMBED-ONLY on the server, so every admitted chunk carries a vector and
// a BM25 document and the client ships segments for the graph like any other; the
// ranked read simply asks the segment engine (raw_graph_segment_search.go). The
// drain existed only because those graphs shipped no segments, and its cost was
// linear in the document — one request per 500 nodes, on every query.
//
// WHAT SURVIVED THE DELETION IS THE HEADING LADDER, and it needed EXTENDING to
// survive: it used to find its parent nodes already resident, because the caller
// had just drained the entire graph. A segment-backed caller hydrates only its
// hits, so the parents are fetched explicitly here.

// fetchNodesByIDs reads the named nodes in ONE bulk ids[] request through the
// same Execute seam the pivot-edge read beside it uses.
//
// IT IS THE exec-SHAPED COUNTERPART OF hydrateEngineHits, not a duplicate of it.
// That helper takes a GraphCaller plus ranked searchengine.Hits and returns
// stamped SearchResults; this one takes the ExecuteFn and a bare id list and
// returns nodes, which is what a heading lookup needs — it is resolving PARENTS
// of hits, which are not hits and carry no score to stamp.
//
// The server clamps the row count; the caller bounds the id list by the hit count.
func fetchNodesByIDs(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	ids []string,
) ([]*knowledgev1.Node, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Ids:        ids,
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_NODES,
			Limit:      int32(len(ids)),
			SkipTotal:  true,
		}},
		Target: target,
	})
	if err != nil {
		return nil, err
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, fmt.Errorf("discovery search: parent node decode: %w", derr)
	}
	return nodes, nil
}

// rawParentHeadingKey is the synthetic metadata key the resolved parent heading
// is stamped under, so the JSON arm — which copies Node.Metadata verbatim — can
// show it. Synthetic because no emitter writes it; it is derived per query.
const rawParentHeadingKey = "__parent_heading"

// rawGraphParentHeadings resolves each hit's containing section heading with ONE
// bounded pivot-edge read over the hit ids alone — at most k of them, not one
// read per hit — plus ONE bulk hydrate of the parents that read names.
//
// THE PARENT HYDRATE IS THE PRECONDITION THIS FUNCTION USED TO GET FOR FREE, and
// losing it is a real defect rather than a tidy-up. byID once held the WHOLE
// graph, because the caller had just drained it, so every parent a CONTAINS edge
// named was already resident. A segment-backed caller hydrates only its HITS, so
// a hit whose section did not itself rank has no parent in byID — and the heading
// silently disappears for exactly those hits. Intermittently, which is worse than
// a clean loss: it reads as a data problem in the collected document rather than
// as a missing read.
//
// So the ladder now runs in two stages: collect the DISTINCT parent ids the edges
// name, skip any already in byID (a parent that was itself a hit), and issue ONE
// bulk ids[] read for the remainder. Bounded by the hit count, since a hit has at
// most one CONTAINS parent, and skipped entirely when nothing is missing — which
// is what keeps a whole-graph caller at its original round-trip count.
//
// CONTAINS needs no case canonicalization: both emitters write the kgtypes
// constant verbatim, so the stored edge type is already the value compared here.
//
// ONE HOP ONLY. This resolves the IMMEDIATELY containing section, which for a
// paragraph is the most specific heading available. It is deliberately not a
// full heading path — do not iterate upward.
//
// A hit with no parent maps to the empty string, and an absent heading renders
// as absent rather than being invented.
func rawGraphParentHeadings(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	hitIDs []string,
	byID map[string]*knowledgev1.Node,
) (map[string]string, error) {
	headings := make(map[string]string, len(hitIDs))
	if len(hitIDs) == 0 {
		return headings, nil
	}
	hits := make(map[string]struct{}, len(hitIDs))
	for _, id := range hitIDs {
		hits[id] = struct{}{}
	}

	edges, err := drainParentContainsEdges(ctx, exec, target, hitIDs)
	if err != nil {
		return nil, err
	}
	// Stage two: hydrate the parents byID does not already hold.
	byID, err = hydrateMissingParents(ctx, exec, target, edges, hits, byID)
	if err != nil {
		return nil, err
	}
	collectParentHeadings(edges, hits, byID, headings)
	return headings, nil
}

// drainParentContainsEdges issues the CONTAINS pivot-edge read over the hit ids,
// paged, and returns every edge the pages yielded.
//
// The plan Limit and the drain's edgeCap are the same number twice on purpose:
// the Limit is what the server enforces, the cap is what the drain uses to
// notice it was enforced.
func drainParentContainsEdges(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	hitIDs []string,
) ([]knowledgev1.Edge, error) {
	return paging.DrainPivotEdges(hitIDs, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:          idPage,
					ReturnMode:   knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					Limit:        int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand: paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
					Selection:    &knowledgev1.Selection{EdgeTypes: []string{string(kgtypes.EdgeContains)}},
				}},
				Target: target,
			})
			if rerr != nil {
				return nil, false, rerr
			}
			decoded, derr := engine.DecodeEdges(resp)
			if derr != nil {
				return nil, false, fmt.Errorf("discovery search: parent edge decode: %w", derr)
			}
			return decoded, resp.GetTruncated(), nil
		})
}

// containedHitID reports the hit id a CONTAINS edge points at. ok is false for
// any edge that is not a CONTAINS edge into one of this read's hits — the one
// admission test both edge loops apply, held in one place so they cannot drift.
//
// CONTAINS needs no case canonicalization: both emitters write the kgtypes
// constant verbatim, so the stored edge type is already the value compared here.
func containedHitID(e *knowledgev1.Edge, hits map[string]struct{}) (string, bool) {
	if e.GetType() != string(kgtypes.EdgeContains) {
		return "", false
	}
	to := e.GetToId()
	if _, isHit := hits[to]; !isHit {
		return "", false
	}
	return to, true
}

// hydrateMissingParents issues the ONE bulk ids[] read for the distinct parent
// ids the edges name that byID does not already hold, and returns the merged
// view. Skipped entirely when nothing is missing, which is what keeps a
// whole-graph caller at its original round-trip count.
//
// The caller's byID belongs to the caller; parents are folded into a LOCAL view
// so a hydrate for one query never mutates a map the renderer reads. When
// nothing is missing the caller's own map is returned unchanged.
func hydrateMissingParents(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	edges []knowledgev1.Edge,
	hits map[string]struct{},
	byID map[string]*knowledgev1.Node,
) (map[string]*knowledgev1.Node, error) {
	missing := make([]string, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for i := range edges {
		e := &edges[i]
		if _, ok := containedHitID(e, hits); !ok {
			continue
		}
		pid := e.GetFromId()
		if pid == "" {
			continue
		}
		if _, known := byID[pid]; known {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		missing = append(missing, pid)
	}
	if len(missing) == 0 {
		return byID, nil
	}
	parents, perr := fetchNodesByIDs(ctx, exec, target, missing)
	if perr != nil {
		return nil, perr
	}
	merged := make(map[string]*knowledgev1.Node, len(byID)+len(parents))
	maps.Copy(merged, byID)
	for _, p := range parents {
		if p.GetId() != "" {
			merged[p.GetId()] = p
		}
	}
	return merged, nil
}

// collectParentHeadings writes each hit's containing heading into headings,
// preferring the parent's SymbolName and falling back to its `heading` metadata.
// A hit whose parent is unknown, or whose parent carries neither, is left ABSENT
// rather than given an invented heading.
func collectParentHeadings(
	edges []knowledgev1.Edge,
	hits map[string]struct{},
	byID map[string]*knowledgev1.Node,
	headings map[string]string,
) {
	for i := range edges {
		e := &edges[i]
		hitID, ok := containedHitID(e, hits)
		if !ok {
			continue
		}
		parent, known := byID[e.GetFromId()]
		if !known {
			continue
		}
		if heading := parent.GetSymbolName(); heading != "" {
			headings[hitID] = heading
			continue
		}
		if heading := kgtypes.Value(parent, "heading"); heading != "" {
			headings[hitID] = heading
		}
	}
}
