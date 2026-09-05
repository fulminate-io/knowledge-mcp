// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"maps"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_query_webpdf.go serves the query tool's web/pdf arms: the ranked
// read and the graph stats read.
//
// WHY THE RANKING IS SEGMENT-BACKED. Raw web and pdf graphs are enrolled
// EMBED-ONLY: the collector already extracted the searchable text, so nothing is
// summarized, but every admitted chunk carries a vector AND a BM25 document and
// the client ships segments for the graph like any other. So the ranked read
// simply asks the segment engine, on both arms, and a raw document is hybrid
// searchable — which is what makes it possible to FIND the passages worth
// extracting instead of walking the whole document.
//
// It used to drain the entire graph over the wire and rank it in memory on every
// query, because the graphs were never embedded and shipped no segments. That
// path is gone, not left beside this one; composeRawGraphSegmentSearch
// (raw_graph_segment_search.go) replaces it at a fixed four round trips.

// routeWebPDFClient claims the web/pdf ranked-text, stats and modules shapes,
// and passes every remaining index-free op through unhandled.
//
// A ranked text read is served by composeRawGraphSegmentSearch. WHICH SHAPES
// COUNT AS ONE IS NOT DECIDED HERE: the rule is segmentSearchClaimMode
// (search_mode_contract.go), the SAME predicate the knowledge and
// registered-custom arms claim through, so the three surfaces cannot drift about
// what a caller's mode means. That is a widening — this arm used to restrict
// itself to mode empty or text, so query(graph:"pdf", mode:"hybrid") fell through
// to a generic deny while the search tool served the same call by silently
// running BM25. The two surfaces disagreeing about one feature was the defect.
//
// mode:"vector" STILL FALLS THROUGH on the QUERY rail, and that is inherited
// rather than chosen here: segmentSearchClaimMode deliberately does not claim it,
// and its own doc says why. The search tool routes by graph rather than through
// the predicate, so search(graph:"pdf", mode:"vector") IS served — the same
// asymmetry registered custom graphs already ship.
//
// mode=stats is served by renderGraphStatsBody — it
// reached nothing before, because no intercept claimed it and "stats" is not an
// engine-reducible mode, so it met the generic deny. mode=modules is served by
// webPDFModules: it IS engine-reducible, so it previously fell through and
// rendered the bare GRAPH_NAMES envelope, whose sync_time comes from a
// different stamper than the collect and therefore cannot answer how stale a
// raw graph is. Everything else — by-id getNode, type-browse — still returns
// (false, _) so the engineDispatch path serves it, since compileQuery lowers
// those to ById/Match and never to a server search.
func routeWebPDFClient(ctx context.Context, deps ClientDeps, a queryArgs, raw json.RawMessage) (bool, kgtools.ToolResult) {
	if a.Mode == "stats" {
		return true, webPDFStats(ctx, deps, a, raw)
	}
	if a.Mode == "modules" {
		return true, webPDFModules(ctx, deps, a, raw)
	}
	// THE ID SIGNAL IS THE SINGULAR ONE, AND THE DIVERGENCE FROM THE
	// REGISTERED-CUSTOM SIBLING IS DELIBERATE — do not "align" it. That arm passes
	// `a.ID != "" || len(a.IDs) > 0`. Folding ids[] into the lookup signal here
	// would make a text+ids payload stop being CLAIMED, so it would reach the
	// by-ids path and the search text would be silently dropped: a caller reading
	// rows back has no way to tell their query was never run. A plural ids[] payload
	// must stay claimed precisely so accountQueryParams refuses it BY NAME —
	// armWebPDFSearch declares ids REJECTED in the query-arm registry, and
	// TestQueryArmParity/armWebPDFSearch/ids is the fence that proves it.
	mode, claimed := segmentSearchClaimMode(a.Mode, a.Text != "" || len(a.Queries) > 0, a.ID != "")
	if !claimed {
		return false, kgtools.ToolResult{} // index-free op → engineDispatch serves it.
	}
	if err := accountQueryParams(armWebPDFSearch, raw); err != nil {
		return true, errorResult(err.Error())
	}
	return true, composeRawGraphSegmentSearch(ctx, deps, deps.SegmentManager(),
		kgtypes.GraphType(a.Graph), a.Name, segmentSearchArgs{
			Query:  practiceQueryText(a),
			Limit:  int(a.Limit),
			Format: a.Format,
			Fields: a.Fields,
			Mode:   mode,
		})
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
	sel := &knowledgev1.GraphSelector{Graph: a.Graph, Name: a.Name}
	extra := map[string]string{
		"collector_schema_version": rawGraphRootStamps(
			ctx, statsExecOf(sc), sel, rawGraphRootType(a.Graph)).schemaVersion,
	}
	return renderGraphStatsBody(ctx, sc, sel, header, a, extra)
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
	// THE mode:vector REFUSAL IS GONE FROM HERE, AND ITS PROPERTY IS NOT DROPPED.
	// It refused on the grounds that "raw web and pdf graphs are never embedded
	// (zero-LLM)", which the embed-only enrollment makes a falsehood — these graphs
	// DO carry vectors now, so a vector search is a legitimate request to serve.
	// What survives is the narrower and still-true refusal: a vector search with no
	// EMBEDDER configured must say so rather than serve zero rows, and that check
	// now lives in composeRawGraphSegmentSearch beside the arm resolution, where it
	// fires before the engine is asked.
	seg.Query = query
	if seg.Format == "" {
		seg.Format = a.Format
	}
	if len(seg.Fields) == 0 {
		seg.Fields = a.Fields
	}
	if seg.Limit <= 0 {
		seg.Limit = int(a.Limit)
	}
	return composeRawGraphSegmentSearch(ctx, deps, deps.SegmentManager(),
		kgtypes.GraphType(graph), named.Name, seg)
}
