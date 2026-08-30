// SPDX-License-Identifier: Apache-2.0

// intercept_checks_search.go — the ranked-search arm for the checks graph, served
// on BOTH rails.
//
// SERVING BOTH RAILS IS NOT OPTIONAL. Retiring the checks refusal on the search
// rail alone would leave query(graph:"checks", text:...) falling through to the
// generic embed+dispatch tail, which renders the server's rows under a search-mode
// footer ASSERTING that a retrieval arm answered. That is the exact defect the
// unranked-builtin refusal was written to close, and reintroducing it under a new
// name would be worse than the refusal it replaced.
//
// THE SELECTOR RULE IS THE SINGLETON ONE, APPLIED DIRECTLY. checks is a BUILTIN
// singleton, so the registered-graph selector validator is structurally wrong for
// it twice over: it admits only registered custom types, and it then requires the
// instance name to appear in the collected-graph-names list, which a graph
// addressing no name can never satisfy. This arm therefore applies the singleton
// rule itself and reaches the shared ranked body directly.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// composeChecksSearch runs the ranked-search arm for the checks graph.
//
// A NON-EMPTY INSTANCE NAME IS REFUSED RATHER THAN IGNORED. The graph consumes no
// instance field — the corpus loader deliberately sends an empty name for exactly
// this reason, because the server's selector policy REJECTS a set name — so an arm
// that silently dropped one would disagree with the loader about what the selector
// means and hand back a result labeled for a graph the caller thinks they named.
func composeChecksSearch(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, name string, a segmentSearchArgs) kgtools.ToolResult {
	if name != "" {
		return errorResult(string(kgtypes.GraphChecks) + " search: the checks graph is a singleton and addresses no instance name, " +
			"so name=" + name + " cannot be honored — omit it, and narrow one language WITHIN the graph via meta:{\"language\":\"go\"}")
	}
	return composeSegmentGraphSearch(ctx, deps, mgr, kgtypes.GraphChecks, "", a)
}

// checksSearchArm decodes the SEARCH tool's payload for the checks arm.
//
// It decodes twice for the reason the custom-graph arm does: searchArgs is the
// client-side mirror of the server search struct and carries the instance-key
// Name field, while segmentSearchArgs is the struct every segment arm reads its
// wire fields through. Using both is what keeps the two tools' segment arms unable
// to disagree about which params exist.
func checksSearchArm(ctx context.Context, deps ClientDeps, raw json.RawMessage) kgtools.ToolResult {
	var ca searchArgs
	if err := json.Unmarshal(raw, &ca); err != nil {
		return errorResult(string(kgtypes.GraphChecks) + " search: decode args: " + err.Error())
	}
	var sa segmentSearchArgs
	if err := json.Unmarshal(raw, &sa); err != nil {
		return errorResult(string(kgtypes.GraphChecks) + " search: decode args: " + err.Error())
	}
	// The queries[] merge overrides Query rather than riding along, matching the
	// custom-graph arm.
	sa.Query = searchReducibleQueryText(searchReducibleArgs{Query: ca.Query, Queries: ca.Queries})
	return composeChecksSearch(ctx, deps, deps.SegmentManager(), ca.Name, sa)
}

// checksShapeIsForeign names the checks payload shapes the QUERY rail's arm does
// NOT serve, so the chain hands them to the paths that do.
//
// IT IS THE practiceShapeIsForeign LESSON APPLIED HERE. Without the decline, a
// checks BROWSE (types:["finding","example"]) and a checks BY-ID read would fall
// into the ranked arm and come back as a clean render of a different operation —
// the most misleading routing failure available, because nothing in the response
// marks it wrong. Both shapes must keep working exactly as they do today, and the
// browse in particular is the one the refusal used to point callers at.
func checksShapeIsForeign(a queryArgs) bool {
	return a.Mode == "metadata_stats" || a.ID != "" || len(a.IDs) > 0
}

// routeChecksQueryClient serves the QUERY rail's checks text search.
func routeChecksQueryClient(ctx context.Context, deps ClientDeps, a queryArgs) (bool, kgtools.ToolResult) {
	if checksShapeIsForeign(a) {
		return false, kgtools.ToolResult{}
	}
	if a.Text == "" {
		return false, kgtools.ToolResult{} // browse / stats / modules keep their own paths.
	}
	// The SHARED definition of which shapes are a text search — the same predicate
	// the knowledge and custom-graph arms claim through (registeredGraphSearchShape
	// is a thin pass to it), including the id-selector precedence that keeps a
	// by-id read a lookup when text rides along.
	if _, claimed := registeredGraphSearchShape(a); !claimed {
		return false, kgtools.ToolResult{}
	}
	// THE ARGS ARE BUILT FROM queryArgs, NEVER DECODED OFF THE QUERY PAYLOAD. The
	// query tool's schema declares no `query` or `half_life` field, and the claim
	// census refuses any struct decoded off a query payload that carries a wire
	// field the schema does not declare — a schema-invisible param is one callers
	// cannot discover and the accounting gate cannot reject. The custom-graph query
	// arm builds its args the same way and through the same mapper, so the two
	// query-rail segment arms cannot disagree about how a query becomes a search.
	return true, composeChecksSearch(ctx, deps, deps.SegmentManager(), a.Name, registeredGraphQueryToSearchArgs(a))
}
