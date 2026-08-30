// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_query_builtin_stats.go serves query(mode:"stats") for the two
// built-in graphs that had no stats arm — checks and transformers — and refuses a
// graph VALUE that names no graph at all.
//
// BEFORE THIS ARM both cases met the same generic engine deny: "stats" is not an
// engine-reducible mode, and no intercept claimed either graph, so a caller
// asking for real stats and a caller making a typo got one indiscriminate
// message. mode:stats now works uniformly.
//
// TWO CASES, DELIBERATELY DISTINCT:
//   - the graph is REAL but had no stats arm (checks, transformers) → SERVED;
//   - the graph value names NO graph ("all", a typo) → refused naming the value
//     AND the accepted vocabulary, per BAD INPUT ALWAYS ERRORS.
//
// A real graph belonging to another arm is DECLINED, not refused: the per-graph
// stats arms (knowledge, practice, cloud/cicd, code, web/pdf) own their own
// shapes and this arm must never shadow them.

// InterceptQueryBuiltinStats claims query(mode:"stats") for checks and
// transformers, and refuses an unknown graph value on the same mode.
func InterceptQueryBuiltinStats(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "stats" {
		return false, kgtools.ToolResult{}
	}
	// ONE SWITCH holds the served membership, on the unrankedBuiltinRefusalFor
	// precedent: a graph admitted here without a body cannot exist, because the
	// body IS the case arm.
	switch a.Graph {
	case string(kgtypes.GraphChecks), string(kgtypes.GraphTransformers):
		return true, builtinGraphStats(ctx, deps, a, params.Arguments)
	}
	if res, unknown := unknownGraphVocabularyRefusal(ctx, deps, a.Graph); unknown {
		return true, res
	}
	return false, kgtools.ToolResult{} // a real graph another arm owns.
}

// builtinGraphStats serves the checks / transformers stats read.
//
// THE SELECTOR CARRIES THE WIRE SPELLING, NOT THE INTERNAL KEY. renderGraphStatsBody
// issues a wire Stats RPC and its sample enrichment issues wire Execute reads
// against the same selector; it consults no client-side working-set or segment
// map. workingset.CanonicalInstanceName answers the INTERNAL question — it
// collapses "" to the default instance name for the single-instance families —
// and applying it here would send "default" on a wire that must carry "" for
// checks, which is exactly the confident-zero seam the two-names rule was written
// to close. Apply it only where a client-side keyed map is being read.
func builtinGraphStats(ctx context.Context, deps ClientDeps, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	// THE TWO GRAPHS DIFFER ON `name`, and the difference is the selector policy
	// rather than a special case. checks is a SINGLETON whose policy carries no
	// instance field and rejects a set name, so "" is the only spelling a reader
	// can send and the arm must not require one. transformers carries a REAL
	// instance name, so a nameless call is refused pointing at the enumeration —
	// the same answer, in the same words, the served web/pdf arm already gives.
	if a.Graph == string(kgtypes.GraphTransformers) && a.Name == "" {
		return errorResult(a.Graph + ` stats: name is required — a transformers graph is keyed by its ` +
			`bucket name and has no default instance; use mode:"modules" to list the available ` +
			a.Graph + ` graphs`)
	}
	sc, res, ok := statsSeamFor(deps, a.Graph)
	if !ok {
		return res
	}
	if err := accountQueryParams(armBuiltinGraphStats, raw); err != nil {
		return errorResult(err.Error())
	}
	return renderGraphStatsBody(ctx, sc, &knowledgev1.GraphSelector{Graph: a.Graph, Name: a.Name},
		builtinStatsHeader(a.Graph, a.Name), a)
}

// builtinStatsHeader is the markdown heading each served graph opens with.
// checks renders as a bare family name because it is a singleton — "One graph,
// so the family name IS the instance name", the same rule queryGraphLabelFor
// applies to its label.
func builtinStatsHeader(graph, name string) string {
	if graph == string(kgtypes.GraphChecks) {
		return "## Checks Graph"
	}
	return "## Transformers Graph: " + name
}

// unknownGraphVocabularyRefusal reports whether the graph value names no graph at
// all, and if so returns the refusal naming the value and the accepted set.
//
// AN UNREADABLE REGISTRY IS REPORTED, NEVER SILENTLY DECLINED. The distinction
// registeredGraphTypeNames draws is real — "this type is not registered" and
// "whether it is registered is unknown" mean opposite things — but the honest
// response to the second is to say so, exactly as validateRegisteredGraphSelector
// does. Declining instead would hand the call back to the generic engine deny,
// which is the indiscriminate message this arm exists to remove: a degraded lane
// that hides the inability rather than reporting it.
//
// An EMPTY graph is not unknown: it means the knowledge default, which its own
// arm claims well before this one. A REGISTERED custom type declines — no stats
// arm serves one today, and inventing one here would be a routing decision this
// arm does not own.
func unknownGraphVocabularyRefusal(ctx context.Context, deps ClientDeps, graph string) (kgtools.ToolResult, bool) {
	if graph == "" || kgtypes.IsBuiltinGraphType(graph) {
		return kgtools.ToolResult{}, false
	}
	registered, err := registeredGraphTypeNames(ctx, deps)
	if err != nil {
		return errorResult(fmt.Sprintf("query(stats): unsupported graph type %q: %s",
			graph, unreadableRegistryVocabulary(err))), true
	}
	if slices.Contains(registered, graph) {
		return kgtools.ToolResult{}, false
	}
	return errorResult(fmt.Sprintf("query(stats): unsupported graph type %q: %s",
		graph, acceptedGraphVocabulary(registered))), true
}
