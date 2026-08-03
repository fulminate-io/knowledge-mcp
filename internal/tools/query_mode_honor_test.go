// SPDX-License-Identifier: Apache-2.0

package tools

// query_mode_honor_test.go is the query-tool and custom-graph half of the mode
// contract. The search tool's half lives in search_mode_honor_test.go.
//
// Like its sibling, every test begins with t.Setenv("VOYAGE_API_KEY", "").
// These arms never construct a reranker, so the Setenv buys machine
// independence rather than a changed code path — but keylessness is enforced
// here for the same reason it is enforced there: a precondition that is assumed
// rather than established is a precondition that silently stops holding.

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// registeredParityEmbedCalls reaches the stub embedder's call counter that
// newRegisteredParityDeps constructs internally and does not return.
// interceptDeps.emb is the BinaryEmbedder interface and stubEmbedder is a value
// type carrying the counter pointer, so the assertion is valid in-package.
//
// Deliberately NOT solved by widening newRegisteredParityDeps: it has eleven
// call sites, one of them under a landed parity fence, and churning all of them
// for a value a type assertion already provides trades real risk for no gain.
func registeredParityEmbedCalls(t *testing.T, deps *interceptDeps) *atomic.Int64 {
	t.Helper()
	emb, ok := deps.emb.(stubEmbedder)
	require.True(t, ok, "the parity fixture wires a stubEmbedder")
	return emb.calls
}

// TestInterceptQueryKnowledgeSearch_ModeTextIsBM25Only pins the query tool to
// the same mode contract the search tool honors: mode:"text" embeds nothing and
// says so in the footer.
func TestInterceptQueryKnowledgeSearch_ModeTextIsBM25Only(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, embedCalls := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
		"graph": "knowledge", "mode": "text", "text": "gate56-probe",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	assert.Equal(t, int64(0), embedCalls.Load(), "mode:text must not embed on the query arm either")
	assert.Empty(t, mgr.lastVec, "no vector may reach the segment engine under mode:text")
	assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_")
}

// TestInterceptQueryKnowledgeSearch_ModeHybridStillEmbeds is a CHARACTERIZATION
// CONTROL, green before and after, and it guards the highest-risk
// over-application in this change: the query arm's claim predicate maps BOTH
// "hybrid" and the empty default onto the same internal value, so a suppression
// keyed on that collapsed value would silently make hybrid AND every
// default-mode search BM25-only. All three shapes must keep embedding.
func TestInterceptQueryKnowledgeSearch_ModeHybridStillEmbeds(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"explicit_hybrid", map[string]any{"graph": "knowledge", "mode": "hybrid", "text": "x"}},
		{"default_mode", map[string]any{"graph": "knowledge", "text": "x"}},
		{"default_mode_with_type_filter", map[string]any{"graph": "knowledge", "text": "x", "type": "finding"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, mgr, embedCalls := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

			handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, tc.args))
			require.True(t, handled)
			require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

			assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "%s must still embed", tc.name)
			assert.NotEmpty(t, mgr.lastVec, "%s must still drive the vector arm", tc.name)
		})
	}
}

// TestRegisteredGraphSearch_ModeTextIsBM25Only drives the custom-graph arm
// through BOTH entry points. This is not redundant: the query path routes the
// caller's mode through a claim predicate that collapses it, while the search
// path decodes the mode verbatim off the wire — so only a two-sided test proves
// both builders carry what the caller actually asked for.
func TestRegisteredGraphSearch_ModeTextIsBM25Only(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")

	t.Run("query_tool", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())
		embedCalls := registeredParityEmbedCalls(t, deps)

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

		assert.Equal(t, int64(0), embedCalls.Load(), "mode:text must not embed on the custom-graph query arm")
		assert.Empty(t, mgr.lastVec)
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_")
	})

	t.Run("search_tool", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())
		embedCalls := registeredParityEmbedCalls(t, deps)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "query": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

		assert.Equal(t, int64(0), embedCalls.Load(), "mode:text must not embed on the custom-graph search arm")
		assert.Empty(t, mgr.lastVec)
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_")
	})
}

// TestRegisteredGraphSearch_ModeHybridStillEmbeds is the custom-graph twin's
// characterization control: green before and after, failing only if the
// suppression over-applies and takes hybrid with it.
func TestRegisteredGraphSearch_ModeHybridStillEmbeds(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")

	t.Run("query_tool", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())
		embedCalls := registeredParityEmbedCalls(t, deps)

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "hybrid", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

		assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "hybrid still embeds")
		assert.NotEmpty(t, mgr.lastVec)
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: vector+text_")
	})

	t.Run("search_tool", func(t *testing.T) {
		deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())
		embedCalls := registeredParityEmbedCalls(t, deps)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "hybrid", "query": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

		assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "hybrid still embeds")
		assert.NotEmpty(t, mgr.lastVec)
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: vector+text_")
	})
}
