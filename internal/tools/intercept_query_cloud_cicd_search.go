// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_query_cloud_cicd_search.go holds the cloud/cicd RANKED-SEARCH arm,
// split out of intercept_query_cloud_cicd.go so that file stays under the
// 500-line cap while the browse and stats arms grow their truncation, totals and
// format:"json" disclosure. Same package, same routing — InterceptQueryCloudCICD
// and search.go call into here unchanged.

// resourceQueryText picks the ranked-search text from the query/text fields
// (mirrors practiceQueryText — text wins, else the first of queries[]).
func resourceQueryText(a queryArgs) string {
	if a.Text != "" {
		return a.Text
	}
	if len(a.Queries) > 0 {
		return a.Queries[0]
	}
	return ""
}

// composeResourceSearchClient runs the cloud/cicd ranked-search arm against the
// CLIENT per-account engine — the exact mirror of composePracticeSearchClient,
// keyed on Account instead of Language and rendered via the SCORED
// engine.RenderResourceSearch (NOT the node/browse renderers). Embed the query
// client-side (so the HNSW arm is exercised), Manager.Search(GraphCloud/CICD,
// account, …) → RRF, then ONE RETURN_MODE_NODES hydrate. A nil embedder degrades
// to the BM25 arm; an empty/un-collected account (no segments) renders zero
// results cleanly — graceful empty, NOT an error.
func composeResourceSearchClient(ctx context.Context, deps ClientDeps, mgr SegmentSearcher, kind resourceGraphKind, account, query, format string) kgtools.ToolResult {
	// The embed error is CAPTURED, not discarded: a failed embed degrades this
	// search to the BM25 arm alone, and without the disclosure a caller cannot tell
	// a degraded hybrid search from a healthy one by looking at rows.
	queryVec, embErr := embedQueryForArm(ctx, deps, query)
	hits, err := mgr.Search(ctx, kgtypes.GraphType(kind.graph), account, query, queryVec, knowledgeSearchDefaultLimit)
	if err != nil {
		return errorResult(kind.graph + " search: client engine: " + err.Error())
	}
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), hydrateSelector{Graph: kind.graph, Account: account}, hits)
	if err != nil {
		return errorResult(kind.graph + " search: hydrate: " + err.Error())
	}
	modeLabel := segmentSearchModeLabel(query != "", len(queryVec) > 0)
	if len(results) == 0 && embErr != nil {
		// An empty result set is where the degrade is most misleading: it reads as
		// "nothing matched" when the semantic arm never ran at all.
		return errorResult(fmt.Sprintf(
			"%s search: no results, and the semantic arm did not run: %s; this search was BM25-only.",
			kind.graph, embErr.Error()))
	}
	if format == "json" {
		// resource_type (and the rest of the resource node metadata) rides through
		// renderJSON's verbatim Metadata copy — no per-path projection needed.
		return engine.RenderForCaller(query, results, "json", nil, modeLabel)
	}
	return engine.RenderResourceSearch(kind.render, account, query, results, modeLabel)
}
