// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_practice_linkage.go is the client-side claim for the practice
// and linkage per-graph query shapes the server routePracticeQuery /
// routeLinkageTarget served (cmd/knowledge-server/tools/tools_query_practice.go,
// tools_query.go, tools_query_linkage.go).
//
// practice shapes:
//   - list-graphs   (no language): enumerate practice graphs
//     (RETURN_MODE_GRAPH_NAMES Execute + Stats RPC counts).
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Practice Graph: <lang>").
//   - search        (text + language): generic search Execute → RenderPracticeResults.
//
// linkage shapes:
//   - list-graphs   (no id/text/mode): enumerate linkage graphs + topology hint.
//   - mode=stats    : Stats RPC → RenderStatsBreakdown ("## Linkage Graph") +
//     the proxy-by-foreign_graph breakdown.
//   - id getNode    : Execute ByID → node render.
//   - ranked text search: RETIRED → rankedSearchRetiredResult("linkage").
//
// web/pdf shapes: ranked text search is RETIRED (rankedSearchRetiredResult); every
// index-free op (by-id getNode, type-browse, mode=stats, mode=modules) passes
// through unhandled to the engineDispatch path (compileQuery lowers them to
// ById/Match/GRAPH_NAMES — no RETURN_MODE_SEARCH).

// InterceptQueryPracticeLinkage claims query(graph in {practice,linkage}).
func InterceptQueryPracticeLinkage(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch a.Graph {
	case "practice":
		sc, res, ok := statsSeamFor(deps, "practice")
		if !ok {
			return true, res
		}
		return true, routePracticeClient(context.Background(), deps, sc, a)
	case "linkage":
		sc, res, ok := statsSeamFor(deps, "linkage")
		if !ok {
			return true, res
		}
		return true, routeLinkageClient(context.Background(), sc, a)
	case "web", "pdf":
		// web/pdf ranked text is retired without touching the wire — no gc needed.
		return routeWebPDFClient(a)
	default:
		return false, kgtools.ToolResult{}
	}
}

// statsSeamFor resolves the statsRPC seam for the practice/linkage arms, returning
// a legible error result (ok=false) when the graph client or stats seam is absent.
func statsSeamFor(deps ClientDeps, graph string) (statsRPC, kgtools.ToolResult, bool) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, errorResult(graph + ": graph client unavailable"), false
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return nil, errorResult(graph + ": stats seam unavailable"), false
	}
	return sc, kgtools.ToolResult{}, true
}

// routeWebPDFClient retires ONLY the ranked-text-search shape for the web/pdf raw
// graphs and passes EVERY index-free op through unhandled. web/pdf are
// SkipsLLMProcessing (embed-forbidden) stage-1 intermediates a translator turns
// into knowledge nodes (which ARE client-searchable), so their raw ranked search
// is retired rather than migrated (no BM25-only axis). A text
// search (text or queries[], no id / specialized mode) returns the retired result;
// everything else (by-id getNode, type-browse, mode=stats, mode=modules) returns
// (false,_) so the engineDispatch path serves it (compileQuery lowers those to
// ById/Match/GRAPH_NAMES — never RETURN_MODE_SEARCH).
func routeWebPDFClient(a queryArgs) (bool, kgtools.ToolResult) {
	isRankedText := (a.Text != "" || len(a.Queries) > 0) &&
		a.ID == "" &&
		(a.Mode == "" || a.Mode == "text")
	if isRankedText {
		return true, rankedSearchRetiredResult(a.Graph)
	}
	return false, kgtools.ToolResult{} // index-free op → engineDispatch serves it.
}

// routePracticeClient dispatches the three practice shapes.
func routePracticeClient(ctx context.Context, deps ClientDeps, gc statsRPC, a queryArgs) kgtools.ToolResult {
	// (1) No language → list practice graphs.
	if a.Language == "" {
		return listPracticeGraphs(ctx, deps)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "practice", Language: a.Language}})
		if err != nil {
			return errorResult(fmt.Sprintf("practice %q graph stats failed: %s", a.Language, err.Error()))
		}
		stats := resp.GetGraphStats()
		var sb strings.Builder
		fmt.Fprintf(&sb, "## Practice Graph: %s\n\n", a.Language)
		sb.WriteString(engine.RenderStatsBreakdown(stats))
		if a.Samples {
			samples := fetchPracticeSamples(ctx, gc.Execute, a.Language, stats)
			var sampleSB strings.Builder
			engine.RenderSampleNames(&sampleSB, stats, samples)
			sb.WriteString(sampleSB.String())
		}
		return textResult(sb.String())
	}
	// (3) search/browse with language.
	query := practiceQueryText(a)

	// (3a) language:"all" → scatter-gather fan-out across every loaded practice
	// graph (kills the silent-0). The empty-language list-graphs BROWSE above is
	// preserved; only the explicit "all" sentinel fans out.
	if a.Language == "all" {
		return composePracticeSearchFanOut(ctx, deps, deps.SegmentManager(), query)
	}

	// (3b) Route a specific-language practice search through the per-language CLIENT
	// engine (Manager.Search → RRF) + hydration UNCONDITIONALLY — the segment Manager
	// is always wired in the real client, so there is no server RETURN_MODE_SEARCH
	// fallback. list-graphs (arm 1) + stats/sample shapes (arm 2) are unchanged —
	// only the ranked search arm reroutes. An un-collected practice graph (no
	// segments) renders zero results cleanly.
	return composePracticeSearchClient(ctx, deps, deps.SegmentManager(), a.Language, query)
}

// composePracticeSearchClient runs the practice ranked-search arm against the
// CLIENT per-language engine: embed the query client-side (so the HNSW arm is
// exercised), Manager.Search(GraphPractice, language, …) → RRF, then ONE
// RETURN_MODE_NODES hydrate, rendered via the same RenderPracticeResults shape
// as the server arm. A nil embedder degrades to the BM25 arm; an empty graph
// (segments not yet built) renders zero results cleanly.
func composePracticeSearchClient(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, language, query string) kgtools.ToolResult {
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil && query != "" {
		if vec, err := emb.EmbedBinary(ctx, query); err == nil && len(vec) > 0 {
			queryVec = vec
		}
	}
	hits, err := mgr.Search(ctx, kgtypes.GraphPractice, language, query, queryVec, knowledgeSearchDefaultLimit)
	if err != nil {
		return errorResult("practice search: client engine: " + err.Error())
	}
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), hydrateSelector{Graph: "practice", Language: language}, hits)
	if err != nil {
		return errorResult("practice search: hydrate: " + err.Error())
	}
	return engine.RenderPracticeResults(language, query, results)
}

// composePracticeSearchFanOut is the scatter-gather practice search across ALL
// loaded practice graphs — the prong that kills the language:"all" silent-0.
// It mirrors composeCodeSearchMultiRepo (intercept_query_code_search.go): the
// graph set is enumerated DYNAMICALLY via listGraphNamesOfType (no hardcoded
// language list), the query is embedded EXACTLY ONCE up front and the vector
// reused for every per-graph Search (no N-embed fan-out), each graph is searched
// in PARALLEL under a NumCPU-bounded pool, then per-graph hits are score-merged
// (sorted desc, capped at knowledgeSearchDefaultLimit) and rendered with
// per-graph attribution via RenderPracticeFanOut. An empty practice-graph set
// renders a clean "no graphs" result rather than a silent zero.
func composePracticeSearchFanOut(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, query string) kgtools.ToolResult {
	names, err := listGraphNamesOfType(ctx, deps, "practice")
	if err != nil {
		return errorResult("practice fan-out: resolve graphs: " + err.Error())
	}
	if len(names) == 0 {
		return textResult("No practice graphs found.")
	}

	// Embed the query a SINGLE time up front; the vector is reused for every
	// per-graph Search so the HNSW arm is exercised without an N-embed fan-out.
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil && query != "" {
		if vec, embErr := emb.EmbedBinary(ctx, query); embErr == nil && len(vec) > 0 {
			queryVec = vec
		}
	}

	type graphResult struct {
		graph   string
		results []engine.SearchResult
	}
	var (
		mu  sync.Mutex
		all []graphResult
		wg  sync.WaitGroup
	)
	gc := deps.GraphCaller()
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for _, name := range names {
		wg.Add(1)
		go func(language string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hits, searchErr := mgr.Search(ctx, kgtypes.GraphPractice, language, query, queryVec, knowledgeSearchDefaultLimit)
			if searchErr != nil || len(hits) == 0 {
				return
			}
			results, hydErr := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: "practice", Language: language}, hits)
			if hydErr != nil || len(results) == 0 {
				return
			}
			mu.Lock()
			all = append(all, graphResult{graph: language, results: results})
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	// Merge per-graph hits, tag each with its source graph, sort by score desc,
	// cap to the default limit (mirrors mergeMultiRepoResults).
	merged := make([]engine.PracticeFanOutHit, 0)
	for _, gr := range all {
		for _, r := range gr.results {
			merged = append(merged, engine.PracticeFanOutHit{Graph: gr.graph, Result: r})
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Result.Score > merged[j].Result.Score })
	if len(merged) > knowledgeSearchDefaultLimit {
		merged = merged[:knowledgeSearchDefaultLimit]
	}

	searched := make([]string, len(names))
	copy(searched, names)
	sort.Strings(searched)
	return engine.RenderPracticeFanOut(query, searched, merged)
}

// practiceQueryText picks the search text from the query/text fields.
func practiceQueryText(a queryArgs) string {
	if a.Text != "" {
		return a.Text
	}
	if len(a.Queries) > 0 {
		return a.Queries[0]
	}
	return ""
}

// rankedSearchRetiredResult is the graph-neutral "ranked text search retired"
// result returned for the raw/derived graphs whose ranked search is NOT migrated
// to the client (linkage / web / pdf). The graph label names the graph so the
// message is specific. linkage proxies + web/pdf raw graphs carry no unique
// client-indexable content (web/pdf are SkipsLLMProcessing/embed-forbidden and are
// stage-1 intermediates a translator turns into knowledge nodes, which ARE
// client-searchable; linkage denormalizes source-graph text). The design
// drops raw ranked search rather than build a BM25-only axis. The index-free ops
// for these graphs — list-graphs / stats / getNode / traverse / proxy
// read-through / browse — are UNAFFECTED. Defined here, reused by both tools.
func rankedSearchRetiredResult(graph string) kgtools.ToolResult {
	return textResult(fmt.Sprintf(
		"Ranked text search for the %s graph is retired. The %s graph carries no "+
			"unique client-indexable content, so it has no ranked search index. "+
			"Its other operations still work: list-graphs, stats, get-node-by-id, "+
			"traverse, and browse.",
		graph, graph))
}

// routeLinkageClient dispatches the linkage shapes.
func routeLinkageClient(ctx context.Context, gc statsRPC, a queryArgs) kgtools.ToolResult {
	// (1) list-graphs: no id/text/mode.
	if a.ID == "" && a.Text == "" && a.Mode == "" && len(a.Queries) == 0 {
		return listLinkageGraphs(ctx, gc)
	}
	// (2) mode=stats.
	if a.Mode == "stats" {
		return linkageStatsClient(ctx, gc)
	}
	// (3) id getNode.
	if a.ID != "" {
		resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: a.ID}},
			Target: &knowledgev1.GraphSelector{Graph: "linkage"},
		})
		if err != nil {
			return errorResult(fmt.Sprintf("node %s not found in linkage graph", a.ID))
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil || len(nodes) == 0 {
			return errorResult(fmt.Sprintf("node %s not found in linkage graph", a.ID))
		}
		return engine.RenderGenericNode(nodes[0], "linkage")
	}
	// (4) ranked text search RETIRED. linkage proxies denormalize
	// source-graph text and carry no unique client-indexable content, so there is
	// no client linkage search index. The index-free ops above (list-graphs, stats
	// + proxy breakdown, id getNode) — and proxy read-through — are unaffected.
	return rankedSearchRetiredResult("linkage")
}

// linkageStatsClient renders the linkage stats body + the proxy-by-foreign_graph
// breakdown (one extra Match(NodeProxy) Execute — bounded by the proxy set, the
// linkage-specific enrichment the server linkageStats appended).
func linkageStatsClient(ctx context.Context, gc statsRPC) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: &knowledgev1.GraphSelector{Graph: "linkage"}})
	if err != nil {
		return errorResult("linkage graph stats failed: " + err.Error())
	}
	stats := resp.GetGraphStats()
	var sb strings.Builder
	sb.WriteString("## Linkage Graph\n\n")
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	sb.WriteString(renderLinkageProxyBreakdown(ctx, gc))
	return textResult(sb.String())
}

// renderLinkageProxyBreakdown fetches the proxy nodes (one Match(NodeProxy)
// Execute, bounded by the proxy set) and renders the proxy-by-foreign_graph
// breakdown the server linkageStats appended. Returns "" when there are no
// proxies / the fetch fails (degrade gracefully — the stats body still renders).
func renderLinkageProxyBreakdown(ctx context.Context, gc statsRPC) string {
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeProxy)},
		}},
		Target: &knowledgev1.GraphSelector{Graph: "linkage"},
	})
	if err != nil {
		return ""
	}
	proxies, derr := engine.DecodeNodes(resp)
	if derr != nil || len(proxies) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, n := range proxies {
		fg := kgtypes.Value(n, "foreign_graph")
		if fg == "" {
			fg = "unknown"
		}
		counts[fg]++
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("\n### Proxy Breakdown\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "- %s: %d proxies\n", k, counts[k])
	}
	return sb.String()
}

// listPracticeGraphs enumerates the loaded practice graphs (RETURN_MODE_GRAPH_NAMES
// Execute via listGraphNamesOfType + per-graph Stats counts).
func listPracticeGraphs(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	names, err := listGraphNamesOfType(ctx, deps, "practice")
	if err != nil {
		return errorResult("practice list-graphs failed: " + err.Error())
	}
	if len(names) == 0 {
		return textResult("No practice graphs found.")
	}
	gc := deps.GraphCaller()
	sc, ok := gc.(statsRPC)
	if !ok {
		return errorResult("practice list-graphs: stats seam unavailable")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Practice graphs (%d):\n\n", len(names))
	for _, name := range names {
		nodes, edges := graphCounts(ctx, sc, "practice", name)
		fmt.Fprintf(&sb, "- **%s** — %d nodes, %d edges\n", name, nodes, edges)
	}
	sb.WriteString("\nUse `query({ \"graph\": \"practice\", \"language\": \"go\" })` to browse a specific practice graph.")
	return textResult(sb.String())
}

// listLinkageGraphs enumerates the loaded linkage graphs + the topology hint.
func listLinkageGraphs(ctx context.Context, gc statsRPC) kgtools.ToolResult {
	// The linkage graph is a single instance (empty name); fetch its counts.
	nodes, edges := graphCounts(ctx, gc, "linkage", "")
	if nodes == 0 && edges == 0 {
		return textResult("No linkage graph found. Linkage graphs are created by the tier-1 linker when code-to-cloud relationships are detected.")
	}
	var sb strings.Builder
	sb.WriteString("Linkage graph:\n\n")
	fmt.Fprintf(&sb, "- %d nodes, %d edges\n", nodes, edges)
	return textResult(sb.String())
}

// fetchPracticeSamples fetches up to 2 sample nodes per node type for the
// practice stats sample enrichment (bounded by node-type count).
func fetchPracticeSamples(ctx context.Context, exec engine.ExecuteFn, language string, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
			}},
			Target: &knowledgev1.GraphSelector{Graph: "practice", Language: language},
		})
		if err != nil {
			continue
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil || len(nodes) == 0 {
			continue
		}
		samples[kgtypes.NodeType(nt)] = nodes
	}
	return samples
}
