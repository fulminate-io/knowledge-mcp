// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// webpdf_discovery.go holds the discovery core for raw document graphs: drain the
// whole graph in bounded pages, project every node onto the five documented BM25
// field keys, rank them in memory, and resolve each hit's containing heading.
//
// THE RANKING IS ZERO-LLM AND ENTIRELY CLIENT-SIDE. Raw web and pdf graphs are
// excluded from the summarizer and embedder, so they carry no vectors and there
// is nothing to search on the vector arm. What they DO carry is text, and BM25
// over that text is a complete answer to "what is in this document" — so the
// scorer is the shared in-memory BM25 format, reused rather than rewritten.
//
// EVERY QUERY RE-DERIVES ALL OF IT. There is no cache and none is added here:
// each call re-drains, re-projects, re-tokenizes and re-builds. That is the
// accepted cost of ranking a per-document bounded graph, and it is what the
// logs-search read already does. Nobody should read the first call's cost as a
// warm-up that a later call avoids.

// rawDiscoveryNodeScanCap bounds how many nodes one discovery search will rank.
//
// THE VALUE IS DERIVED, NOT MEASURED: it is taken from the sibling
// engine.CorrelationsEdgeScanCap, which is a measured EDGE budget over a
// different corpus. No raw-graph node count was measured for it.
//
// Size it against the PEAK, which is a multiple of the corpus rather than one
// copy of it. At the composer's high-water mark the process holds five
// concurrent representations of the same nodes: the drained node slice, the
// SegmentDoc projection, the searchengine.Document slice, BM25's build-time
// posting maps, and the encoded blob the published segment reads through. Only
// the posting maps are released before the search runs — the node slice is held
// to the end, because the renderer reads hit nodes and parent headings out of it.
const rawDiscoveryNodeScanCap = 50000

// rawParentHeadingKey is the synthetic metadata key the resolved parent heading
// is stamped under, so the JSON arm — which copies Node.Metadata verbatim — can
// show it. Synthetic because no emitter writes it; it is derived per query.
const rawParentHeadingKey = "__parent_heading"

// drainRawGraphNodes reads EVERY node of a raw document graph in bounded id-keyset
// pages, and refuses to exceed scanCap.
//
// THE CEILING IS AN ERROR, NOT A TRUNCATION. When the drained count would pass
// scanCap this returns an error naming the graph, the ceiling and the remedy —
// it never returns a partial slice with a nil error. Ranking a prefix while
// presenting it as a whole-graph ranking is a manufactured answer: the caller
// asked what is in the document and would be told, with no signal, what is in
// part of it. The named inability plus the remedy is the honest output.
//
// Serial by necessity: page N+1's cursor is page N's last id, so the drain
// cannot be parallelized over an unknown total. One request per 500 nodes.
func drainRawGraphNodes(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	scanCap int,
) ([]*knowledgev1.Node, error) {
	if scanCap <= 0 {
		return nil, fmt.Errorf("discovery search: scanCap must be positive (got %d)", scanCap)
	}
	out := make([]*knowledgev1.Node, 0, paging.BrowsePageSize)
	err := paging.DrainKeysetPagesFunc(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				// An EMPTY Selection is the match-all browse: discovery ranks
				// every node type at once, so there is no type to narrow by.
				Selection: &knowledgev1.Selection{},
				Limit:     int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is
				// empty: presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true,
			}},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		page, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("discovery search: node decode: %w", derr)
		}
		return page, nil
	}, paging.BrowsePageSize, func(page []*knowledgev1.Node) error {
		if len(out)+len(page) > scanCap {
			return fmt.Errorf(
				"discovery search: graph %q holds more than %d nodes, the ceiling for whole-graph client-side ranking; narrow the collect or raise rawDiscoveryNodeScanCap",
				target.GetName(), scanCap)
		}
		out = append(out, page...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rawGraphDocuments projects drained nodes onto the five documented BM25 field
// keys. It is the Node -> SegmentDoc projection ONLY; turning those into
// searchengine.Documents is delegated to the shared pipeline.BuildBM25Documents,
// so raw discovery assembles documents exactly as the writeback and rebuild
// paths do.
//
// ALL FIVE KEYS ARE MAPPED WITH NO PER-SOURCE BRANCH, and that is what makes the
// web/pdf field asymmetry a non-issue. The emitters genuinely disagree about
// where a body lands: web page bodies go to Description, web section headings to
// SymbolName with no body at all, web paragraph and code-block bodies to
// Content; pdf document and chunk bodies both go to Description, with SymbolName
// set only for a section chunk. Mapping every key covers both families at once.
//
// SymbolName is load-bearing rather than decorative: a web section node carries
// no Content and no Description whatsoever, so dropping that key would make
// every section heading in every web graph unrankable — which is precisely the
// question this search exists to answer.
//
// Summary and Keywords are mapped for uniformity though no emitter fills them
// today. They are left in deliberately: an emitter that starts populating either
// gets ranking for free, and a per-field test for a field nothing produces would
// assert against a fixture value that never occurs in production.
func rawGraphDocuments(nodes []*knowledgev1.Node) []pipeline.SegmentDoc {
	docs := make([]pipeline.SegmentDoc, 0, len(nodes))
	for _, n := range nodes {
		fields := make(map[string]string, 5)
		if v := n.GetSymbolName(); v != "" {
			fields[searchengine.FieldSymbolName] = v
		}
		if v := n.GetSummary(); v != "" {
			fields[searchengine.FieldSummary] = v
		}
		if v := n.GetKeywords(); v != "" {
			fields[searchengine.FieldKeywords] = v
		}
		if v := n.GetDescription(); v != "" {
			fields[searchengine.FieldDescription] = v
		}
		if v := n.GetContent(); v != "" {
			fields[searchengine.FieldContent] = v
		}
		docs = append(docs, pipeline.SegmentDoc{NodeID: n.GetId(), Fields: fields})
	}
	return docs
}

// rankRawGraphNodes scores nodes against query and returns the top k hits.
//
// The whole scorer is reused: one BM25 segment is built in memory, its corpus
// stats folded, and the query tokenized once. No embedder, no segment manager,
// no disk — the segment is encoded to bytes and reopened entirely in memory.
//
// An empty query, k<=0, or an empty node set are not failures. A zero-token
// query yields no hits by construction, and an all-empty batch yields an empty
// but searchable segment, so each returns nil hits with a nil error.
func rankRawGraphNodes(nodes []*knowledgev1.Node, query string, k int) ([]searchengine.Hit, error) {
	format := bm25.New()
	segment, err := format.Build(pipeline.BuildBM25Documents(rawGraphDocuments(nodes)))
	if err != nil {
		return nil, fmt.Errorf("discovery search: build BM25 segment: %w", err)
	}
	stats := format.AggregateStats([]searchengine.Segment[bm25.Query, *bm25.CorpusStats]{segment})
	// nil accept: there is no liveDocs filter over a freshly built segment.
	return segment.Search(bm25.NewQuery(query), stats, k, nil), nil
}

// rawGraphParentHeadings resolves each hit's containing section heading with ONE
// bounded pivot-edge read over the hit ids alone — at most k of them, not one
// read per hit.
//
// NO HYDRATE RPC IS ISSUED. Parents are looked up in byID, which the caller
// built from the same whole-graph drain, so the edge read is the only extra
// round trip.
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

	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced.
	edges, err := paging.DrainPivotEdges(hitIDs, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
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
	if err != nil {
		return nil, err
	}

	for i := range edges {
		e := &edges[i]
		if e.GetType() != string(kgtypes.EdgeContains) {
			continue
		}
		if _, isHit := hits[e.GetToId()]; !isHit {
			continue
		}
		parent, known := byID[e.GetFromId()]
		if !known {
			continue
		}
		if heading := parent.GetSymbolName(); heading != "" {
			headings[e.GetToId()] = heading
			continue
		}
		if heading := kgtypes.Value(parent, "heading"); heading != "" {
			headings[e.GetToId()] = heading
		}
	}
	return headings, nil
}
