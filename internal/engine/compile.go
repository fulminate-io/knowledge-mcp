// SPDX-License-Identifier: Apache-2.0

// Package engine is the client-side compiler that translates the reducible
// LLM-facing tool surface (the allowlist: search /
// query-read-modes / traverse / mutate create-update-delete-link-unlink) into
// the declarative proto QueryPlan/MutationPlan carried by Engine.Execute.
//
// Compile is DEFAULT-DENY: it returns ok=true ONLY for a shape it explicitly
// recognizes as reducible; every other shape — the SPECIALIZED set (§B) plus
// thought/charge creates — returns ok=false so the dispatcher falls through to
// the legacy gc.Call wire UNCHANGED.
//
// This package owns intent→plan translation PLUS the type-aware normalization
// moved client-side: per-graph edge casing (canonicalEdgeCasing) and the
// resource_type post-filter (filterByResourceTypePrefix). Multi-query score-sum
// fusion, the unified default limit, the traverse-both union, and depth-1
// hydration stay in the engine. Type-aware mutate(create) body validation is NO
// LONGER a client concern: the server engine enforces it in
// decodeCreate and returns invalidMutation, which Dispatch relays verbatim —
// the engine package no longer imports cmd/knowledge/internal/validate. The
// package imports gen/knowledge/v1 + pkg/store so the bootstrap chokepoint and
// the InterceptSearch/Query tails can import it without a cycle.
package engine

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// Compile translates a reducible LLM-facing tool call into a declarative
// ExecuteRequest. It returns (request, true) ONLY for a shape in the §A
// reducible allowlist; for everything else it returns (nil, false) and the
// caller falls through to the legacy gc.Call wire.
//
// The top-level switch routes by tool name; each sub-compiler applies the
// per-tool default-deny rules (graph=code → ok=false, specialized modes →
// ok=false, etc.).
func Compile(tool string, args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	switch tool {
	case "search":
		return compileSearch(args)
	case "query":
		return compileQuery(args)
	case "traverse":
		return compileTraverse(args)
	case "mutate":
		return compileMutate(args)
	case "delete":
		// The standalone `delete` tool (prune startup runner + the LLM-facing
		// delete tool) lowers by-ids AND prune-by-age onto MUTATION_KIND_DELETE.
		return compileDelete(args)
	default:
		return nil, false
	}
}

// buildTarget maps the selector args common to every reducible tool onto the
// proto GraphSelector (engine.proto:70-77). Empty graph means the knowledge
// graph (the engine treats graph=="" as knowledge). The fields mirror the
// server-side graphSelector struct one-for-one.
func buildTarget(graph, repo, account, name, language, branch string) *knowledgev1.GraphSelector {
	if graph == "" && repo == "" && account == "" && name == "" && language == "" && branch == "" {
		return nil
	}
	return &knowledgev1.GraphSelector{
		Graph:    graph,
		Repo:     repo,
		Account:  account,
		Name:     name,
		Language: language,
		Branch:   branch,
	}
}

// isCodeGraph reports whether the selector targets the code graph, which is
// SPECIALIZED (HandleSearchCode / HandleAnalyzeNode) and
// therefore never reducible on search/query.
//
// Second consumer: compileTraverse requests the edge-metadata carrier on every
// code-graph traversal, because only the code collector emits multi-candidate
// edge groups and only the per-edge Method distinguishes one from N bound edges.
func isCodeGraph(graph string) bool {
	return graph == "code"
}

// base64Decode decodes a base64-encoded query_vector. The engine validates the
// decoded length (must equal 32) and returns
// CodeInvalidArgument on a mismatch — the client does NOT length-check.
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// canonicalEdgeCasing returns the canonical-casing form of a single edge-type
// string for the given graph: code / cloud / cicd / linkage / logs graphs use
// UPPERCASE edge types (CALLS, MOUNTS_SECRET, ...); knowledge / practice / thought
// edges are lowercase (contains, relates-to). The engine uses edge_types AS-GIVEN,
// so the client produces the canonical casing here before it rides the wire.
//
// An empty graph string means knowledge → lowercase, matching the engine's
// envelope-routing empty-graph=knowledge default.
func canonicalEdgeCasing(graph, t string) string {
	upper := graph == "code" || graph == "cloud" || graph == "cicd" ||
		graph == "linkage" || graph == "logs"
	if upper {
		return strings.ToUpper(t)
	}
	return strings.ToLower(t)
}

// canonicalEdgeCasings maps canonicalEdgeCasing over a slice.
// Returns the input unchanged for an empty slice.
func canonicalEdgeCasings(graph string, ts []string) []string {
	if len(ts) == 0 {
		return ts
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = canonicalEdgeCasing(graph, t)
	}
	return out
}
