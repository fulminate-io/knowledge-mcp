// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_query_unranked_builtin.go is the QUERY-tool claim for the ONE builtin
// graph type that carries no ranked index: transformers.
//
// IT USED TO CLAIM TWO. checks now carries segments for its check findings and is
// SERVED on both rails; its fixture example nodes are refused server-side by node
// type, which is what keeps deliberately-wrong code out of ranked results. The
// refusal was retired only once the served arm existed — retiring it first would
// have made ranked search answer a graph with no index and return a confident
// empty result, which is the silent zero the refusal was written to prevent.
//
// IT IS THE SECOND HALF OF ONE DEFECT. The search tool had the identical gap and
// the identical cause — interceptSearchReducibleGraph's custom-graph default
// branch ejects builtins, so a builtin missing from the case list above it was
// claimed by nobody. On this rail the ejecting gate is
// InterceptQueryRegisteredGraphSearch's `a.Graph == "" ||
// kgtypes.IsBuiltinGraphType(a.Graph)`, and what a transformers/checks text query
// fell through to was worse than a bare zero: the generic embed+dispatch tail
// (tools.InterceptQuery) rendered the server's rows under a "_search mode:
// BM25-only_" footer, ASSERTING that a BM25 arm had answered. None can:
// pipeline.bm25ArmEnabledFor gates the BM25 collector on
// kgtypes.HasRebuildableSegments, which still excludes transformers, so it carries
// no BM25 index at all. Two of the four published text modes did not even reach
// that tail — mode:hybrid and mode:recent fell all the way to the generic engine
// deny, which the dispatch-parity harness's headline invariant forbids for any
// published mode.
//
// THE REFUSAL WORDING IS NOT DUPLICATED HERE. Both rails call the SAME
// transformersSearchUnavailableResult (intercept_search_reducible_graph.go), so a
// caller who reaches for either tool is told the same thing about the same graph.
// Two hand-kept copies of a message that names an access path is exactly the shape
// that rots into two different pieces of advice.

// unrankedBuiltinRefusalFor returns the refusal for a graph this arm claims, and
// reports false for any other graph. It is the ONE place the membership is written
// down — a ONE-graph membership since checks was cut over: the claim gate and the
// answer read the same switch, so an arm that claimed a second graph without a
// message for it cannot exist.
func unrankedBuiltinRefusalFor(graph string) (kgtools.ToolResult, bool) {
	switch graph {
	case "transformers":
		return transformersSearchUnavailableResult(), true
	default:
		return kgtools.ToolResult{}, false
	}
}

// InterceptQueryUnrankedBuiltin claims the QUERY-tool text-search shapes for the
// transformers graph and answers them with the graph's self-describing refusal.
//
// ITS GATE IS THE REGISTERED-GRAPH TWIN'S, inverted on one clause: that arm claims
// a NON-builtin graph, this one claims one builtin. Everything else is
// deliberately identical, because the two arms answer the same question about the
// same payload shapes, and any divergence between them would be a routing surprise
// rather than a design.
//
// WHAT IT DECLINES IS AS LOAD-BEARING AS WHAT IT CLAIMS. The refusal hands the
// caller a BROWSE — query(graph:"transformers", name:"recipes", type:"recipe").
// Claiming that browse shape would route the reader of the message straight back
// into the message: a closed loop with no exit, strictly worse than the silent
// zero it replaced. The
// empty-Text bail below is what keeps every index-free op — browse, by-id,
// mode=stats, mode=modules — falling through to the paths that serve them.
//
// THAT SENTENCE WAS ASPIRATIONAL FOR mode=stats UNTIL InterceptQueryBuiltinStats
// LANDED, and the correction is recorded rather than quietly absorbed: this bail
// did let stats fall through, but there was no path serving it, so it fell all the
// way to the generic engine deny. The chain member that now serves it sits
// immediately after this one, which is what makes the sentence true.
//
// IT KEEPS THE UNIFORM CHAIN-MEMBER SIGNATURE and names both leading parameters
// `_`. Uniformity is what lets the chain and the parity harness drive it exactly
// like every sibling with no adapter; the underscores are the statement that this
// arm reads neither a context nor a graph client, because its answer depends on no
// read, no embed and no wire call. A future edit that needs either has to name it,
// which is the moment to ask whether a refusal should be costing one.
func InterceptQueryUnrankedBuiltin(_ context.Context, _ ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	refusal, mine := unrankedBuiltinRefusalFor(a.Graph)
	if !mine {
		return false, kgtools.ToolResult{}
	}
	if hasThoughtQueryFilter(a) {
		return false, kgtools.ToolResult{} // recall/reflect shape stays on the thoughts surface.
	}
	if a.Text == "" {
		return false, kgtools.ToolResult{} // browse / by-id / stats / modules keep their own paths.
	}
	// segmentSearchClaimMode is the SHARED definition of which shapes are a text
	// search — the same predicate the knowledge and custom-graph arms claim through.
	// Reusing it is what makes "this arm refuses exactly the shapes those arms would
	// have served" true by construction rather than by inspection, including the
	// id-selector precedence that keeps a by-id read a lookup when text rides along.
	if _, claimed := segmentSearchClaimMode(a.Mode, a.Text != "", a.ID != "" || len(a.IDs) > 0); !claimed {
		return false, kgtools.ToolResult{}
	}
	if err := accountQueryParams(armUnrankedBuiltinSearchRefused, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	return true, refusal
}
