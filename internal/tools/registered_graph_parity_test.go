// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// registeredParityNodes is the hydrate fixture every filter sub-test below
// drives: TWO nodes with DIFFERENT ids, types, names and metadata. A one-node
// fixture would render identically whether the post-filter ran or not, so each
// filter assertion checks BOTH directions — the kept row present AND the
// dropped row absent.
func registeredParityNodes() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{Id: "h1", Type: "fact", SymbolName: "KeptRow", Metadata: map[string]string{"k": "v"}},
		{Id: "h2", Type: "note", SymbolName: "DroppedRow", Metadata: map[string]string{"k": "other"}},
	}
}

// registeredParityHits ranks both fixture rows, h1 first.
func registeredParityHits() []searchengine.Hit {
	return []searchengine.Hit{{ID: "h1", Score: 0.9}, {ID: "h2", Score: 0.8}}
}

// newRegisteredParityDeps wires the standard intercept fixture (recording
// GraphClient for the ids[] hydrate read, fake segment engine, stub embedder)
// and hands back the segment searcher so a sub-test can read lastK.
//
// The graph-type registry knows "hellograph" and the graph catalog reports
// "demo" collected, which is what every sub-test below selects: these parity
// assertions are about the render tail, so the selector has to RESOLVE before
// the arm gets there (validateRegisteredGraphSelector refuses an unregistered
// type or an uncollected instance ahead of the search).
func newRegisteredParityDeps(
	t *testing.T, nodes []*knowledgev1.Node, hits []searchengine.Hit,
) (*interceptDeps, *fakeSegmentSearcher) {
	t.Helper()
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(nodes...))
	handler.graphNames = []string{"demo"}
	mgr := &fakeSegmentSearcher{hits: hits}
	return &interceptDeps{
		gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr,
		gtCRUD: registeredGraphTypes("hellograph"),
	}, mgr
}

// TestRegisteredGraphSearchTwin_ParityGaps drives the two registered
// custom-graph search entry points — InterceptQueryRegisteredGraphSearch (the
// query tool) and InterceptSearch (the search tool) — and asserts the parity
// behaviors the knowledge search arm already has: a supplied type/types/meta
// filter is APPLIED rather than dropped, the default-mode claim gate does not
// decline a filtered text search, limit reaches the segment engine, the fields
// projection reaches the render, mode:recent actually reranks, and the
// search-mode footer is disclosed rather than emitted empty.
//
// Every assertion is written against observable BEHAVIOR through the two
// exported entry points, so it stays valid whatever internal shape the fix
// takes.
func TestRegisteredGraphSearchTwin_ParityGaps(t *testing.T) {
	t.Run("default_mode_type_text_is_claimed", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, _ := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph",
			"name":  "demo",
			"type":  "fact",
			"text":  "world",
		}))
		require.True(t, handled,
			"a default-mode text search carrying a type filter is a SEARCH, not a browse — "+
				"declining it drops the call onto an engine path that hard-errors for a custom graph")
	})

	t.Run("type_filter_applied_under_explicit_mode", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world", "type": "fact",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, "KeptRow", "the type-matching row survives the filter")
		assert.NotContains(t, body, "DroppedRow", "a non-matching row must be filtered out, not rendered")
	})

	t.Run("types_plural_filter_applied", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		// BOTH spellings supplied with CONFLICTING values: the plural set wins and
		// the singular is not also applied, mirroring the precedence the knowledge
		// arm's builder uses. Written as a union this would keep both rows.
		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
			"type": "note", "types": []string{"fact"},
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, "KeptRow", "the plural set selects the fact row")
		assert.NotContains(t, body, "DroppedRow", "the singular spelling must not widen the plural set")
	})

	t.Run("meta_filter_applied_under_explicit_mode", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
			"meta": map[string]string{"k": "v"},
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, "KeptRow", "the metadata-matching row survives the filter")
		assert.NotContains(t, body, "DroppedRow", "a row whose metadata value differs must be filtered out")
	})

	t.Run("limit_reaches_the_segment_engine", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world", "limit": 3,
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		require.Equal(t, 3, mgr.lastK, "the caller's limit is the segment engine's top-k, not a constant default")
	})

	t.Run("fields_projection_reaches_the_render", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
			"format": "json", "fields": []string{"id"},
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, "h1", "the projected id is emitted")
		assert.NotContains(t, body, "KeptRow",
			"a fields:[id] projection must drop every unrequested key, symbol_name included")
	})

	t.Run("recent_mode_applies_the_temporal_rerank", func(t *testing.T) {
		now := time.Now()
		nodes := []*knowledgev1.Node{
			{Id: "h1", Type: "fact", SymbolName: "RecentRow", UpdatedAt: now.UnixNano()},
			{Id: "h2", Type: "fact", SymbolName: "StaleRow", UpdatedAt: now.Add(-3650 * 24 * time.Hour).UnixNano()},
		}
		// BASE order is the OPPOSITE of the recency order: the stale row ranks
		// higher before the rerank. The half-life boost (score *= 1+2^(-ageDays/30))
		// doubles the fresh row and leaves the decade-old row essentially untouched,
		// so a rerank that ran inverts the pair with margin.
		hits := []searchengine.Hit{{ID: "h2", Score: 0.9}, {ID: "h1", Score: 0.8}}
		deps, _ := newRegisteredParityDeps(t, nodes, hits)

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "recent", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		require.Contains(t, body, "RecentRow")
		require.Contains(t, body, "StaleRow")
		assert.Less(t, strings.Index(body, "RecentRow"), strings.Index(body, "StaleRow"),
			"mode:recent is claimed, so it must also apply the UpdatedAt half-life rerank")
	})

	t.Run("search_mode_footer_is_disclosed", func(t *testing.T) {
		// Drives mode:hybrid — the FUSED arm — because that is the payload whose
		// honest footer names both arms. This sub-test guards disclosure, not
		// suppression; its BM25-only sibling below guards the other direction.
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "hybrid", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.NotContains(t, body, "_search mode: _", "the footer is emitted unconditionally, so an empty marker ships an empty footer")
		assert.Contains(t, body, "_search mode: vector+text_", "the custom-graph arm runs the same fusion the knowledge arm discloses")
	})

	t.Run("search_mode_footer_bm25_only_under_mode_text", func(t *testing.T) {
		// The other direction: mode:text runs BM25 alone, and the footer must say
		// so. A footer that named a vector arm here would be describing retrieval
		// that did not happen.
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.NotContains(t, body, "_search mode: _", "an empty marker ships an empty footer")
		assert.Contains(t, body, "_search mode: BM25-only_", "mode:text suppresses the vector arm, so the footer must not claim one")
	})

	t.Run("search_tool_types_filter_applied", func(t *testing.T) {
		deps, _ := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "query": "world", "types": []string{"fact"},
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, "KeptRow", "the type-matching row survives the filter on the search tool too")
		assert.NotContains(t, body, "DroppedRow", "the search tool's types filter must narrow the custom-graph result")
	})

	t.Run("search_tool_limit_reaches_the_segment_engine", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "query": "world", "limit": 3,
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))
		require.Equal(t, 3, mgr.lastK, "the search tool's limit is the segment engine's top-k, not a constant default")
	})
}
