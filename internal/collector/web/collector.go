// SPDX-License-Identifier: Apache-2.0

// Package web collects raw typed graph records from HTML pages.
//
// The collector fetches a page, strips chrome with go-readability, walks
// the DOM, and emits page/section/paragraph/code_block/list/list_item/table/
// link/image/blockquote nodes with contains/references/cites edges into a
// per-source graph keyed by source_name slug. The graph is marked
// SkipsLLMProcessing=true so the raw content never hits summarizer/embedder —
// a downstream stage-2 translator consumes the raw graph.
//
// CrawlOptions flow through context (see WithCrawlOptions) so the collector
// package remains domain-free.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	collector.Register(&WebCollector{})
}

// WebCollector collects typed raw-graph records from web pages. See package
// doc for the emitted node shape and per-source graph layout.
type WebCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *WebCollector) Name() string { return "web" }

// Collect executes the BFS crawl configured by the CrawlOptions carried in
// ctx (see WithCrawlOptions): seed URLs feed a breadth-first queue bounded
// by MaxDepth, MaxPages and FollowPatterns; every fetched page is walked
// and emitted as typed raw-graph nodes. After the crawl completes,
// internal-link REFERENCES edges (Phase 3 emits them with a placeholder
// "web:url:<absolute>" target) are rewired to the real page node ID when
// the target was itself crawled; unvisited targets are re-classified as
// external cites so downstream consumers never see a dangling placeholder.
//
// The returned CollectResult targets GraphWebRaw keyed on opts.Source so
// collector.Collect writes to ~/.knowledge/web/<source>.bin.
func (c *WebCollector) Collect(
	ctx context.Context,
	_ string,
	_ collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	opts, ok := crawlOptionsFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("web collector: CrawlOptions missing from context (use WithCrawlOptions)")
	}
	opts = opts.ApplyDefaults()
	if err := ValidateCrawlOptions(opts); err != nil {
		return nil, fmt.Errorf("web collector: %w", err)
	}

	fc := newFetchClient(opts.UserAgent, opts.PolitenessMs)
	pages, urlToID, matNodes, matEdges, err := crawl(ctx, fc, opts)
	if err != nil {
		return nil, fmt.Errorf("web collector: crawl: %w", err)
	}

	var nodes []*knowledgev1.Node
	var edges []kgwire.BatchEdge
	for _, p := range pages {
		pn, pe := emitFromPage(p)
		nodes = append(nodes, pn...)
		edges = append(edges, pe...)
	}
	edges = resolveInternalLinks(edges, urlToID)

	// Append materialized github nodes/edges. These sit alongside the
	// HTML page nodes in the same web/<source-slug> graph so downstream
	// recipes can walk both.
	nodes = append(nodes, matNodes...)
	edges = append(edges, matEdges...)
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphWebRaw,
		GraphName: opts.Source,
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}

// resolveInternalLinks rewrites page-level internal-link REFERENCES edges
// emitted by emit_nodes.go's emitLinks. Those edges carry a placeholder
// ToID of the form "web:url:<absolute>" and Evidence metadata including
// rel="internal" / url=<absolute>. After a crawl we know which of those
// absolute URLs actually produced a page node, so we can:
//
//   - rewrite ToID to the real page node ID when the URL is in urlToID
//     (the link resolves to a crawled page — edge becomes an in-graph link);
//   - downgrade rel from "internal" to "external" otherwise (the URL was
//     same-host but the crawl did not visit it — budget / regex filter /
//     fetch failure — so the edge is a cite, not an in-graph reference).
//
// External edges (rel="external") are left untouched; they are already
// cites against an unresolved URL.
func resolveInternalLinks(edges []kgwire.BatchEdge, urlToID map[string]string) []kgwire.BatchEdge {
	for i := range edges {
		e := &edges[i]
		if e.Type != kgtypes.EdgeReferences {
			continue
		}
		md := parseEdgeMeta(e.Evidence)
		if md["rel"] != "internal" {
			continue
		}
		rawURL := md["url"]
		if id, ok := urlToID[rawURL]; ok && id != "" {
			e.ToID = id
			continue
		}
		md["rel"] = "external"
		e.Evidence = jsonMeta(md)
	}
	return edges
}

// parseEdgeMeta unmarshals an edge.Evidence JSON blob into a string map.
// Empty / invalid blobs return an empty map so callers can safely index.
func parseEdgeMeta(raw string) map[string]string {
	m := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}
