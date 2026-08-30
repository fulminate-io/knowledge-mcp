// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"maps"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_query_webpdf.go serves the query tool's web/pdf arms: the ranked
// text read, composed client-side, and the graph stats read.
//
// WHY THE RANKING IS CLIENT-SIDE AND BM25-ONLY. Raw web and pdf graphs skip LLM
// processing, so they are never embedded and never ship search segments — there
// is nothing on the vector arm to search and no server-side ranked index to ask.
// What they carry is text, and BM25 over the drained text answers the question a
// reader actually has about a collected document: which parts of it mention the
// thing they are looking for.

// routeWebPDFClient claims the web/pdf ranked-text and stats shapes, and passes
// every remaining index-free op through unhandled.
//
// A ranked text read (text or queries[], no id, no specialized mode) is served
// by composeRawGraphSearch. mode=stats is served by renderGraphStatsBody — it
// reached nothing before, because no intercept claimed it and "stats" is not an
// engine-reducible mode, so it met the generic deny. Everything else — by-id
// getNode, type-browse, mode=modules — returns (false, _) so the engineDispatch
// path serves it, since compileQuery lowers those to ById/Match/GRAPH_NAMES and
// never to a server search.
func routeWebPDFClient(ctx context.Context, deps ClientDeps, a queryArgs, raw json.RawMessage) (bool, kgtools.ToolResult) {
	if a.Mode == "stats" {
		return true, webPDFStats(ctx, deps, a, raw)
	}
	isRankedText := (a.Text != "" || len(a.Queries) > 0) &&
		a.ID == "" &&
		(a.Mode == "" || a.Mode == "text")
	if !isRankedText {
		return false, kgtools.ToolResult{} // index-free op → engineDispatch serves it.
	}
	if err := accountQueryParams(armWebPDFSearch, raw); err != nil {
		return true, errorResult(err.Error())
	}
	return true, composeRawGraphSearch(ctx, deps, a.Graph, a.Name, practiceQueryText(a), a.Format, a.Fields, int(a.Limit))
}

// webPDFStats serves query(graph:web|pdf, mode:"stats").
//
// A NAMELESS CALL IS REFUSED, NOT ENUMERATED, and the refusal is checked BEFORE
// the Stats RPC so it costs no round trip. Enumeration already has a surface —
// mode:"modules" lists the collected raw graphs — and a second surface answering
// the same question is worse than a refusal that points at the first one.
func webPDFStats(ctx context.Context, deps ClientDeps, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	if a.Name == "" {
		return errorResult(a.Graph + ` stats: name is required — a raw graph is keyed by its source slug and has no default instance; use mode:"modules" to list the available ` + a.Graph + ` graphs`)
	}
	sc, res, ok := statsSeamFor(deps, a.Graph)
	if !ok {
		return res
	}
	if err := accountQueryParams(armWebPDFStats, raw); err != nil {
		return errorResult(err.Error())
	}
	header := "## Web Graph: " + a.Name
	if a.Graph == "pdf" {
		header = "## PDF Graph: " + a.Name
	}
	return renderGraphStatsBody(ctx, sc, &knowledgev1.GraphSelector{Graph: a.Graph, Name: a.Name}, header, a)
}

// composeRawGraphSearch runs the whole ranked read: drain the graph, rank it in
// memory, resolve each hit's containing heading, render.
//
// THERE IS DELIBERATELY NO PipelineReady GATE AND NO nil-Manager GATE HERE. The
// segment-backed arms carry those because they dereference the segment Manager;
// this path never touches it, so such a gate would assert a precondition this
// code has no use for. Do not "restore" one.
//
// A MISSING GRAPH IS AN ERROR, NOT AN EMPTY RESULT. A graph name that does not
// resolve fails at the server's source-name lookup, the drain's first read
// returns that error, and it is propagated as an error result. The only clean
// zero this path produces is the narrow one it should: a graph that resolves and
// holds nodes, none of which match the query. Nothing here catches a resolve
// failure and continues — that would turn "this graph is not collected" into
// "this document says nothing about your query", which are the two answers a
// reader most needs told apart.
func composeRawGraphSearch(
	ctx context.Context,
	deps ClientDeps,
	graph, name, query, format string,
	fields []string,
	limit int,
) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult(graph + " discovery search: graph client unavailable")
	}
	if name == "" {
		return errorResult(graph + ` discovery search: name is required (the source slug the graph was collected under); use mode:"modules" to list the collected graphs`)
	}
	target := &knowledgev1.GraphSelector{Graph: graph, Name: name}

	nodes, err := drainRawGraphNodes(ctx, gc.Execute, target, rawDiscoveryNodeScanCap)
	if err != nil {
		return errorResult(err.Error())
	}

	k := limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	hits, err := rankRawGraphNodes(nodes, query, k)
	if err != nil {
		return errorResult(err.Error())
	}

	byID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		byID[n.GetId()] = n
	}
	hitIDs := make([]string, 0, len(hits))
	for _, h := range hits {
		hitIDs = append(hitIDs, h.ID)
	}
	headings, err := rawGraphParentHeadings(ctx, gc.Execute, target, hitIDs, byID)
	if err != nil {
		return errorResult(err.Error())
	}

	rawHits := make([]engine.RawGraphHit, 0, len(hits))
	for _, h := range hits {
		node := byID[h.ID]
		if node == nil {
			continue
		}
		heading := headings[h.ID]
		rawHits = append(rawHits, engine.RawGraphHit{
			Result: engine.SearchResult{
				Node: nodeWithParentHeading(node, heading),
				// Graph and GraphInstance are what let the shared renderer name
				// the source of a hit; an unstamped result renders anonymously.
				Score:         h.Score,
				Graph:         graph,
				GraphInstance: name,
			},
			ParentHeading: heading,
		})
	}

	if format == "json" {
		results := make([]engine.SearchResult, 0, len(rawHits))
		for _, h := range rawHits {
			results = append(results, h.Result)
		}
		return engine.RenderForCaller(query, results, "json", fields, segmentSearchModeLabel(true, false))
	}
	return engine.RenderRawGraphResults(graph, name, query, rawHits)
}

// nodeWithParentHeading returns n with the resolved heading stamped under the
// synthetic metadata key, so the JSON arm — which copies Node.Metadata verbatim
// — shows it too.
//
// The node is CLONED, never mutated in place: it belongs to the drained slice,
// which the renderer and the parent lookup both read, and a per-query derived
// value has no business being written back onto it. The clone goes through
// proto.Clone rather than a struct copy — a generated message carries internal
// state that must not be copied by value.
func nodeWithParentHeading(n *knowledgev1.Node, heading string) *knowledgev1.Node {
	if heading == "" {
		return n
	}
	clone, ok := proto.Clone(n).(*knowledgev1.Node)
	if !ok {
		return n
	}
	md := make(map[string]string, len(n.GetMetadata())+1)
	maps.Copy(md, n.GetMetadata())
	md[rawParentHeadingKey] = heading
	clone.Metadata = md
	return clone
}

// searchRawGraphArm is the SEARCH tool's web/pdf arm. It lives here beside the
// query tool's arm so both surfaces of one feature read together, and so
// search.go stays under the repo's file-length cap.
//
// The instance key is Name, which searchReducibleArgs does not carry — a raw
// graph is keyed by its source slug. The raw payload is decoded a SECOND time
// for it, exactly as the registered-custom arm does and for the identical
// reason: searchReducibleArgs mirrors the server search struct and is not
// widened to suit one arm.
func searchRawGraphArm(
	ctx context.Context,
	deps ClientDeps,
	graph string,
	raw json.RawMessage,
	query string,
	a searchReducibleArgs,
) kgtools.ToolResult {
	var named searchArgs
	if err := json.Unmarshal(raw, &named); err != nil {
		return errorResult(graph + " search: decode args: " + err.Error())
	}
	var seg segmentSearchArgs
	if err := json.Unmarshal(raw, &seg); err != nil {
		return errorResult(graph + " search: decode args: " + err.Error())
	}
	// A raw graph is never embedded, so a vector search CANNOT run. Refusing
	// loudly is the point: serving zero rows would read as "no matches" when the
	// truth is "this graph has no semantic index at all". Every other mode —
	// text, hybrid, recent — is served through the BM25 path, and the render's
	// footer discloses which arm actually ran.
	if normalizeSegmentSearchMode(seg.Mode) == "vector" {
		return errorResult(graph + " search: mode:vector needs a query embedding, " +
			"but raw web and pdf graphs are never embedded (zero-LLM) — use mode:text or omit mode")
	}
	return composeRawGraphSearch(ctx, deps, graph, named.Name, query, a.Format, a.Fields, int(a.Limit))
}
