// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_search_reducible_graph.go holds the SEARCH tool's per-graph claim
// switch for every reducible graph other than knowledge/code/logs, plus the two
// self-describing refusals that switch returns for the builtins that carry no
// ranked index.
//
// SPLIT OUT OF search.go FOR THE LINE BUDGET. The lefthook file-length gate
// blocks any *.go file over 500 lines and search.go stood at 494, so the two
// builtin claims added here could not land beside it. This arm is the seam the
// file already had: a self-contained unit — one arg struct, one text picker, one
// claim switch — that search.go calls once and never reaches into otherwise. The
// naming follows the package's existing intercept_search_*.go arm files.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// searchReducibleArgs is the slice of the search payload the completeness arms
// read: the graph instance key (account), the query text, and the three
// caller-facing knobs the composers route — format, limit and fields. Mirrors the
// engine.searchArgs fields compileSearch consumes for these graphs.
type searchReducibleArgs struct {
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	Account string   `json:"account"`
	// Format is threaded into the practice/resource composers so the SEARCH-tool
	// reducible arms honor format:"json" (engine.RenderForCaller) like the
	// query-tool arms do.
	Format string `json:"format"`
	// Limit and Fields are the SEARCH-tool siblings of the query-tool arm's
	// a.Limit / a.Fields, threaded into the practice fan-out so this tool routes
	// the caller's row cap and json projection rather than dropping them. flexInt
	// (not int) matches queryArgs, so a host sending "5" as a string is not
	// silently zeroed.
	Limit  flexInt  `json:"limit"`
	Fields []string `json:"fields,omitempty"`
	// Language names ONE practice graph to search. Empty or "all" keeps the
	// scatter-gather default. The engine's own searchArgs has carried this field
	// all along (compile_search.go) — what was missing was the schema declaration
	// and the single-graph branch, which is why the param was refused as unknown
	// rather than dropped.
	Language string `json:"language"`
}

// searchReducibleQueryText picks the search text from the query/queries fields.
func searchReducibleQueryText(a searchReducibleArgs) string {
	if a.Query != "" {
		return a.Query
	}
	if len(a.Queries) > 0 {
		return strings.Join(a.Queries, " ")
	}
	return ""
}

// interceptSearchReducibleGraph claims the SEARCH-tool arms for the reducible
// graphs OTHER than knowledge/code/logs. practice/cloud/cicd are served by the
// CLIENT segment engine; web/pdf are served by the client-computed BM25 read
// over the drained raw graph; linkage, transformers and checks carry no ranked
// index and are REFUSED by name. Returns (false,_) for any other graph
// (knowledge/default flows past to the embed/rerank tail). NO server
// RETURN_MODE_SEARCH is emitted for any claimed graph.
//
// EVERY BUILTIN IS NOW NAMED IN THIS SWITCH, and that completeness is the point
// rather than tidiness. The default branch below serves REGISTERED CUSTOM graphs
// and opens by ejecting anything builtin, so a builtin absent from the switch was
// claimed by nobody: it fell out of the interceptor entirely and compiled to a
// server RETURN_MODE_SEARCH, which the server treats as informational. The caller
// got rows with no error and no disclosure that ranking never ran, and any
// query_vector was discarded with the plan. transformers and checks sat in that
// gap — too builtin for the custom branch, absent from the switch — until they
// were claimed here.
func interceptSearchReducibleGraph(ctx context.Context, deps ClientDeps, graph string, raw json.RawMessage) (bool, kgtools.ToolResult) {
	switch graph {
	case "practice", "cloud", "cicd", "linkage", "web", "pdf", "transformers", "checks":
	default:
		// A CUSTOM graph (non-empty, non-builtin) is claimed here and served by the
		// CLIENT segment engine — its shipped segments ARE the index (the server
		// RETURN_MODE_SEARCH path is retired and returns 0 hits for these graphs).
		// knowledge/code/logs are handled upstream in InterceptSearch.
		//
		// CLAIMED IS NOT VALIDATED: a non-builtin graph reaching this default is a
		// custom-graph SHAPE, not necessarily a registered type — an unregistered
		// string (a typo, or the never-implemented "all") lands here too. The claim
		// is what lets this arm REFUSE such a selector by name;
		// composeRegisteredGraphSearch validates it against the graph-type registry
		// and errors rather than searching. Decode searchArgs (NOT
		// searchReducibleArgs — the custom-graph instance key is the Name field,
		// which searchReducibleArgs lacks) for the (name, query) pair. Anything
		// still empty/builtin falls through to the knowledge/default tail.
		if graph == "" || kgtypes.IsBuiltinGraphType(graph) {
			return false, kgtools.ToolResult{}
		}
		var ca searchArgs
		if err := json.Unmarshal(raw, &ca); err != nil {
			return true, errorResult(graph + " search: decode args: " + err.Error())
		}
		// Decode the SAME raw payload a second time into segmentSearchArgs — what
		// composeKnowledgeSearch does for the knowledge arm — so both tools' segment
		// arms read the same wire fields (types, limit, fields, format, mode)
		// through the same struct and cannot disagree about which params exist.
		// searchArgs is NOT widened for this: it is the client-side mirror of the
		// server search struct, and bending it to one arm's needs breaks the mirror.
		var sa segmentSearchArgs
		if err := json.Unmarshal(raw, &sa); err != nil {
			return true, errorResult(graph + " search: decode args: " + err.Error())
		}
		// The queries[] merge differs from the decoded query field whenever the
		// caller sent `queries`, so it overrides Query rather than riding along.
		sa.Query = searchReducibleQueryText(searchReducibleArgs{Query: ca.Query, Queries: ca.Queries})
		return true, composeRegisteredGraphSearch(ctx, deps, deps.SegmentManager(),
			kgtypes.GraphType(graph), ca.Name, sa)
	}

	// The two no-index graphs are refused BEFORE the arg decode: their answer
	// depends on no field of the payload, and refusing first is what keeps the
	// refusal free of any read, wire call or embed. checks used to be a third; it
	// now carries segments for its check findings and is SERVED below.
	switch graph {
	case "linkage":
		return true, rankedSearchRetiredResult(graph)
	case "transformers":
		return true, transformersSearchUnavailableResult()
	case "checks":
		return true, checksSearchArm(ctx, deps, raw)
	}

	var a searchReducibleArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return true, errorResult(graph + " search: decode args: " + err.Error())
	}
	query := searchReducibleQueryText(a)

	switch graph {
	case "practice":
		// A NAMED language searches THAT graph; anything else fans out.
		//
		// The fan-out is the DEFAULT and stays the default: a scatter-gather over
		// every loaded practice graph, which is what kills the silent-0 that
		// mgr.Search(GraphPractice,"all",…) would otherwise return. "all" is the
		// explicit spelling of that same default, matching the query tool's
		// vocabulary so the two tools cannot disagree about what the word means.
		//
		// The single-graph branch DELEGATES to composePracticeSearchClient — the
		// composer the QUERY tool's practice-search arm already uses. A second
		// per-language practice search would drift from it on ranking and on limit
		// semantics, which is the cross-tool inconsistency this branch removes.
		if a.Language != "" && a.Language != "all" {
			return true, composePracticeSearchClient(ctx, deps, deps.SegmentManager(),
				a.Language, query, a.Format, int(a.Limit), a.Fields)
		}
		return true, composePracticeSearchFanOut(ctx, deps, deps.SegmentManager(), query, a.Format, int(a.Limit), a.Fields)
	case "cloud":
		return true, composeResourceSearchClient(ctx, deps, deps.SegmentManager(), cloudGraphKind, a.Account, query, a.Format)
	case "cicd":
		return true, composeResourceSearchClient(ctx, deps, deps.SegmentManager(), cicdGraphKind, a.Account, query, a.Format)
	default: // web, pdf — client-computed BM25 over the drained raw graph.
		return true, searchRawGraphArm(ctx, deps, graph, raw, query, a)
	}
}

// transformersSearchUnavailableResult is the transformers graph's self-describing
// refusal, on the rankedSearchRetiredResult precedent: state what the graph IS,
// state that ranked search is not on offer for it, and name the access path that
// works — so a caller who reached for search leaves with the call they wanted
// rather than a zero they have to interpret.
//
// THE "NO SEGMENTS" CLAIM IS THE REPO'S OWN PREDICATE, not an inference from the
// empty result: pipeline.bm25ArmEnabledFor gates the BM25 collector on
// kgtypes.HasRebuildableSegments, which excludes transformers, so the graph gets
// no client search segments at all and there is nothing for a ranked query to read.
//
// THE BROWSE NAMED HERE IS THE ONE THE PRODUCT ITSELF DRIVES: recipe
// loadRecipeByName reads the corpus through exactly this (graph, name, type)
// triple against the single recipes bucket, so the path handed to the caller is
// the path the loader depends on rather than one composed for the message.
func transformersSearchUnavailableResult() kgtools.ToolResult {
	return textResult(
		"Ranked search for the transformers graph is not available. The transformers " +
			"graph is the recipe store: DSL transformer bodies held as graph-resident " +
			"recipe nodes in the single \"recipes\" bucket, authored and edited through " +
			"mutate CRUD — mutate(operation:\"create\", graph:\"transformers\", " +
			"type:\"recipe\", name:..., content:...). It carries no client search " +
			"segments, so there is no ranked index for a query to read. Working access " +
			"paths: query(graph:\"transformers\", name:\"recipes\", type:\"recipe\") " +
			"browses every stored recipe, and query(graph:\"transformers\", " +
			"name:\"recipes\", id:\"<recipe-id>\") reads one.")
}
