// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// raw_graph_segment_search.go is the raw-graph (web / pdf) ranked read, served
// from the CLIENT segment engine. It replaces a client-side whole-graph
// drain-and-rank: raw graphs are now enrolled embed-only on the server, so their
// chunks carry vectors AND BM25 documents and the shipped segments can simply be
// asked.
//
// IT IS A SIBLING OF composeSegmentGraphSearch, NOT A CALL INTO IT, and the
// difference is one line at the end. That function finishes through
// finishSegmentSearchRender, the generic post-hydrate tail, which would drop the
// locality context line ("under: <heading> | p. N | #anchor") that makes a raw
// paragraph hit actionable at all — a web paragraph carries no SymbolName and no
// Description, so without the heading the generic renderer prints a bare hex id.
// The raw arm needs the same body with a different tail. Do not "simplify" it
// into the twin.
//
// FOUR ROUND TRIPS, CONSTANT IN CORPUS SIZE, and that is the whole point of the
// change: the graph-existence catalog read, the bulk hit hydrate, the CONTAINS
// pivot-edge read, and the bulk parent hydrate. Serial is correct — each read
// consumes the previous one's ids. The path it replaces drained the entire graph
// in 500-node pages, so its cost was linear in the document.

// composeRawGraphSegmentSearch runs the ranked read for one raw graph: resolve,
// embed if the mode wants it, ask the segment engine, hydrate the hits, resolve
// each hit's containing heading, render with the locality line.
func composeRawGraphSegmentSearch(
	ctx context.Context,
	deps ClientDeps,
	mgr SegmentSearcher,
	gt kgtypes.GraphType,
	name string,
	a segmentSearchArgs,
) kgtools.ToolResult {
	graph := string(gt)
	// Readiness FIRST, ahead of every other gate, for the reason
	// segmentSearchNotReady states: a selector complaint during the bind-first
	// wiring window sends the caller to fix a call that was fine.
	if res, notReady := segmentSearchNotReady(deps, mgr, gt); notReady {
		return res
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult(graph + " discovery search: graph client unavailable")
	}
	// CARRIED VERBATIM from the deleted composer, wording included, so the
	// mode:"modules" remedy survives the deletion. Placed AHEAD of the embed for
	// the same reason the twin places its selector gate there: an unresolvable
	// selector must never bill an embedding.
	if name == "" {
		return errorResult(graph + ` discovery search: name is required (the source slug the graph was collected under); use mode:"modules" to list the collected graphs`)
	}

	// A MISSING GRAPH IS AN ERROR, NOT AN EMPTY RESULT — carried from the deleted
	// composer, which got this refusal for free because a never-collected name
	// failed its drain's very first read. The segment engine has no such read: it
	// returns zero hits for a graph it holds nothing for, which would turn "this
	// graph is not collected" into "this document says nothing about your query".
	// Those are the two answers a reader most needs told apart, so the existence
	// gate is restored explicitly against the collected-graph catalog.
	if res, missing := rawGraphNotCollected(ctx, deps, graph, name); missing {
		return res
	}

	engineName := workingset.CanonicalInstanceName(gt, name)
	mode := normalizeSegmentSearchMode(a.Mode)
	// embedQueryForArm rather than the twin's inline embed-and-discard: the twin
	// has NOT adopted the helper, and reading its body as the pattern on this one
	// point would reintroduce the defect the helper exists to remove. The error is
	// surfaced below on an EMPTY result set, where the degrade is least visible.
	var queryVec []byte
	var embedErr error
	if mode != "text" {
		queryVec, embedErr = embedQueryForArm(ctx, deps, a.Query)
	}
	engineText, engineVec := segmentSearchEngineArms(mode, a.Query, queryVec)
	// THE VECTOR REFUSAL MOVES HERE; IT IS NOT DELETED. Serving zero rows for a
	// vector search with no embedder reads as "no matches" when the truth is
	// "there is no semantic index to ask". Same wording and same reason as
	// composeSegmentGraphSearch's arm. This is the surviving half of the retired
	// TestInterceptSearch_WebPDFVectorModeRefused.
	if mode == "vector" && len(engineVec) == 0 {
		return errorResult(graph + " search: mode:vector needs a query embedding, " +
			"but no embedder is configured — use mode:hybrid or mode:text instead")
	}

	k := a.Limit
	if k <= 0 {
		k = knowledgeSearchDefaultLimit
	}
	hits, err := mgr.Search(ctx, gt, engineName, engineText, engineVec, k)
	if err != nil {
		return errorResult(graph + " search: client engine: " + err.Error())
	}

	// THE WIRE SELECTOR KEEPS THE CALLER'S NAME, never the engine key. A raw graph
	// carries a real instance field so the two agree today, but collapsing them is
	// a defect this repo has paid for three times and the twin spells it out.
	target := &knowledgev1.GraphSelector{Graph: graph, Name: name}
	results, err := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: graph, Name: name}, hits)
	if err != nil {
		return errorResult(graph + " search: hydrate: " + err.Error())
	}
	// The embed failure is disclosed exactly where it would otherwise be invisible:
	// on an empty result set, "no matches" and "the semantic arm never ran" render
	// identically.
	if len(results) == 0 && embedErr != nil {
		return errorResult(fmt.Sprintf(
			"%s search: no results, and the semantic arm never ran — embedding the query failed: %v "+
				"(the BM25 arm's empty result is therefore not evidence the document says nothing)",
			graph, embedErr))
	}

	// THE RECENCY ARM, and it is here because this composer does NOT end in
	// finishSegmentSearchRender, where the sibling arms get it for free. Routing
	// this arm through segmentSearchClaimMode widened its claimed modes to include
	// "recent", so the arm now ACCEPTS a recency request; without this call it
	// would accept one and rank by relevance anyway, which is the worst of the
	// three possible behaviours — a caller reading rows back cannot tell an
	// unhonoured mode from a corpus where the newest chunk really did rank last.
	// Keyed on the NORMALIZED mode, mirroring composeKnowledgeSearch exactly rather
	// than approximating it. HalfLife arrives from the SEARCH rail, which decodes
	// the whole payload; the QUERY rail publishes no half_life param, so it passes
	// zero and computeTemporalScore floors that to the 30-day default — the same
	// value intercept_query_knowledge_search.go hands its own recency arm, so the
	// two query-rail arms decay identically rather than by accident.
	if mode == "recent" {
		applyTemporalRerank(results, a.HalfLife)
	}

	// The label is COMPUTED from what actually reached the engine, replacing the
	// hardcoded BM25-only literal both arms used to assert unconditionally.
	label := segmentSearchModeLabel(engineText != "", len(engineVec) > 0)
	return renderRawGraphHits(ctx, gc.Execute, target, graph, name, a, results, label)
}

// renderRawGraphHits is composeRawGraphSegmentSearch's post-hydrate tail: the
// CONTAINS pivot that resolves each hit's containing heading, the fold of that
// heading onto the node, and the render.
//
// It is the tail the raw arm has INSTEAD of finishSegmentSearchRender, and the
// locality line is the whole reason (see this file's header): a web paragraph
// carries no SymbolName and no Description, so the generic renderer would print
// a bare hex id. Split out of the composer as a unit — the two round trips it
// owns (the pivot-edge read and the bulk parent hydrate) are the last two of the
// header's four, and they are the ones that consume the hydrate's ids.
func renderRawGraphHits(
	ctx context.Context,
	exec engine.ExecuteFn,
	target *knowledgev1.GraphSelector,
	graph, name string,
	a segmentSearchArgs,
	results []engine.SearchResult,
	label string,
) kgtools.ToolResult {
	byID := make(map[string]*knowledgev1.Node, len(results))
	hitIDs := make([]string, 0, len(results))
	for _, r := range results {
		if r.Node == nil {
			continue
		}
		byID[r.Node.GetId()] = r.Node
		hitIDs = append(hitIDs, r.Node.GetId())
	}
	headings, err := rawGraphParentHeadings(ctx, exec, target, hitIDs, byID)
	if err != nil {
		return errorResult(err.Error())
	}

	rawHits := make([]engine.RawGraphHit, 0, len(results))
	for _, r := range results {
		if r.Node == nil {
			continue // hydrateEngineHits already skips a ranked id the hydrate did not return.
		}
		heading := headings[r.Node.GetId()]
		r.Node = nodeWithParentHeading(r.Node, heading)
		rawHits = append(rawHits, engine.RawGraphHit{Result: r, ParentHeading: heading})
	}

	if a.Format == "json" {
		out := make([]engine.SearchResult, 0, len(rawHits))
		for _, h := range rawHits {
			out = append(out, h.Result)
		}
		return engine.RenderForCaller(a.Query, out, "json", a.Fields, label)
	}
	return engine.RenderRawGraphResults(graph, name, a.Query, rawHits, label)
}

// rawGraphNotCollected reports whether the named raw graph is absent from the
// collected-graph catalog, with the refusal to render when it is.
//
// DELEGATE, NOT NEW: listGraphNamesOfType (intercept_query_cloud_cicd.go) is the
// existing RETURN_MODE_GRAPH_NAMES catalog reader that manage(status) and the
// coverage reconcile already use, so this asks the same source of truth about
// which graphs exist rather than inventing a second one.
//
// A CATALOG READ THAT FAILS IS NOT TREATED AS ABSENCE. An unreachable catalog
// tells us nothing about whether the graph exists, and refusing on it would turn
// a transport fault into "your graph is not collected" — so the error is returned
// as itself.
func rawGraphNotCollected(ctx context.Context, deps ClientDeps, graph, name string) (kgtools.ToolResult, bool) {
	names, err := listGraphNamesOfType(ctx, deps, graph)
	if err != nil {
		return errorResult(fmt.Sprintf("%s search: cannot confirm graph %q is collected: %v", graph, name, err)), true
	}
	if slices.Contains(names, name) {
		return kgtools.ToolResult{}, false
	}
	return errorResult(fmt.Sprintf(
		"%s search: graph %s is not collected — this is not the same answer as a search that matched nothing; "+
			`use mode:"modules" to list the collected %s graphs`, graph, name, graph)), true
}
