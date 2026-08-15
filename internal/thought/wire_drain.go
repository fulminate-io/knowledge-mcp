// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// wire_drain.go holds the paged id-KEYSET browse drain: the shared cursor core and
// its two production closures. Split out of wire.go to keep that file under the
// 500-line cap; the drain is one cohesive concern (page cursor, termination,
// dedup) distinct from the single-shot read helpers wire.go carries.

// browsePageSize is the per-page row count for the keyset drain, aliased to the
// paging constant so the page size has exactly one definition. 500 ≈ 13 pages
// for the current ~6102-thought corpus. See paging.BrowsePageSize for why a
// positive limit is load-bearing rather than merely a tuning choice.
const browsePageSize = paging.BrowsePageSize

// drainPages delegates to paging.DrainKeysetPages, which owns the shared id-KEYSET
// drain core and its rationale. Kept as an unexported package-local name because
// both call sites (drainThoughtBrowse below, and the all_types adjacency drain in
// wire_adjacency.go) read more clearly against the thought graph's vocabulary.
func drainPages(fetchPage func(afterID string) ([]*knowledgev1.Node, error), pageSize int) ([]*knowledgev1.Node, error) {
	return paging.DrainKeysetPages(fetchPage, pageSize)
}

// drainThoughtBrowse drains every node of nodeType in bounded id-KEYSET pages via
// the executeViaEngine query seam. Each page sends a POSITIVE limit (pageSize),
// which overrides the engine's browseDefaultLimit cap verbatim, plus the id
// cursor; the drain stops on the first short page. This is the corpus-complete
// replacement for the old single limit:0 browse, which the engine silently
// rewrote to 10 rows.
//
// Every page SETS after_id and never sets Offset. Setting it is what selects the
// keyset browse — including on page 1, where the value is the empty string — and
// the two cursors are mutually exclusive server-side (a plan carrying both is
// rejected with InvalidArgument).
//
// Every page carries skip_total: the drain consumes only the []*Node payloads
// (via drainPages) and never reads Total, so the single-layer executor skips the
// redundant per-page paginating COUNT. Correctness-safe because no drain caller
// reads Total; the multi-layer executor ignores skip_total and keeps its exact
// COUNT(*) OVER() window (the knowledge default graph is single-layer, so drain
// pages take the COUNT-skipping path).
func drainThoughtBrowse(ctx context.Context, gc Caller, nodeType string, pageSize int) ([]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, nil
	}
	return drainPages(func(afterID string) ([]*knowledgev1.Node, error) {
		raw, err := json.Marshal(queryArgs{Type: nodeType, Limit: pageSize, AfterID: &afterID, SkipTotal: true})
		if err != nil {
			return nil, err
		}
		resp, err := executeViaEngine(ctx, gc, "query", raw)
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}, pageSize)
}

// fetchAllThoughtNodes returns every NodeThought in the graph, served from the
// resident corpus cache when src is warm and otherwise drained from the
// type=thought browse in bounded offset pages (drainThoughtBrowse). A nil/cold src
// takes the drain: paging is required because a single limit:0 browse is silently
// capped to browseDefaultLimit(10) rows by the engine, so the drain is what makes
// this read corpus-complete. See thoughtCorpus (loop_corpus.go) for the seam.
func fetchAllThoughtNodes(ctx context.Context, gc Caller, src CorpusSource) ([]*knowledgev1.Node, error) {
	return thoughtCorpus(ctx, gc, src)
}
